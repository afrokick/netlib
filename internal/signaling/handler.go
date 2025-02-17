package signaling

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/koenbollen/logging"
	"github.com/poki/netlib/internal/cloudflare"
	"github.com/poki/netlib/internal/metrics"
	"github.com/poki/netlib/internal/signaling/stores"
	"github.com/poki/netlib/internal/util"
	"go.uber.org/zap"
	"nhooyr.io/websocket"
)

const MaxConnectionTime = 1 * time.Hour

func Handler(ctx context.Context, store stores.Store, cloudflare *cloudflare.CredentialsClient) (*sync.WaitGroup, http.HandlerFunc) {
	manager := &TimeoutManager{
		Store: store,
	}
	go manager.Run(ctx)

	wg := &sync.WaitGroup{}
	return wg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := logging.GetLogger(ctx)
		logger.Debug("upgrading connection")

		ctx, cancel := context.WithTimeout(ctx, MaxConnectionTime)
		defer cancel()

		userAgentLower := strings.ToLower(r.Header.Get("User-Agent"))
		isSafari := strings.Contains(userAgentLower, "safari") && !strings.Contains(userAgentLower, "chrome") && !strings.Contains(userAgentLower, "android")
		acceptOptions := &websocket.AcceptOptions{
			// Allow any origin/game to connect.
			InsecureSkipVerify: true,
		}

		if isSafari {
			acceptOptions.CompressionMode = websocket.CompressionDisabled
		}

		conn, err := websocket.Accept(w, r, acceptOptions)
		if err != nil {
			util.ErrorAndAbort(w, r, http.StatusBadRequest, "", err)
		}

		wg.Add(1)
		defer wg.Done()

		peer := &Peer{
			store: store,
			conn:  conn,

			retrievedIDCallback: manager.Reconnected,
		}
		defer func() {
			logger.Info("peer websocket closed", zap.String("peer", peer.ID))
			conn.Close(websocket.StatusInternalError, "unexpceted closure")

			if !peer.closedPacketReceived {
				// At this point ctx has already been cancelled, so we create a new one to use for the disconnect.
				nctx, cancel := context.WithTimeout(logging.WithLogger(context.Background(), logger), time.Second*10)
				defer cancel()
				manager.Disconnected(nctx, peer)
			}
		}()

		go func() { // Sending ping packet every 30 to check if the tcp connection is still alive.
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if err := peer.Send(ctx, PingPacket{Type: "ping"}); err != nil && !util.IsPipeError(err) {
						logger.Error("failed to send ping packet", zap.String("peer", peer.ID), zap.Error(err))
					}
				case <-ctx.Done():
					return
				}
			}
		}()

		for ctx.Err() == nil {
			var raw []byte
			if _, raw, err = conn.Read(ctx); err != nil {
				util.ErrorAndDisconnect(ctx, conn, err)
			}

			typeOnly := struct{ Type string }{}
			if err := json.Unmarshal(raw, &typeOnly); err != nil {
				util.ErrorAndDisconnect(ctx, conn, err)
			}

			if peer.closedPacketReceived {
				logger.Warn("received packet after close", zap.String("peer", peer.ID), zap.String("type", typeOnly.Type))
				continue
			}

			switch typeOnly.Type {
			case "credentials":
				credentials, err := cloudflare.GetCredentials(ctx)
				if err != nil {
					util.ReplyError(ctx, conn, err)
				} else {
					packet := CredentialsPacket{
						Type:        "credentials",
						Credentials: *credentials,
					}
					if err := peer.Send(ctx, packet); err != nil {
						util.ErrorAndDisconnect(ctx, conn, err)
					}
				}

			case "event":
				params := metrics.EventParams{}
				if err := json.Unmarshal(raw, &params); err != nil {
					util.ErrorAndDisconnect(ctx, conn, err)
				}
				go metrics.RecordEvent(ctx, params)

			case "pong":
				// ignore, ping/pong is just for the tcp keepalive.

			default:
				if err := peer.HandlePacket(ctx, typeOnly.Type, raw); err != nil {
					util.ErrorAndDisconnect(ctx, conn, err)
				}
			}
		}
	})
}
