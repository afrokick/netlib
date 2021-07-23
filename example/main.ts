import { Network } from '../lib'

const n = new Network('d0fe1ca1-7fa0-47ed-9469-6c792f68bae0')
;(window as any).n = n

const inp = document.getElementById('input') as HTMLInputElement
const out = document.getElementById('output') as HTMLTextAreaElement
const log = (text: string): void => {
  console.log(text)
  if (out?.innerText != null) {
    const time = (new Date()).toLocaleTimeString()
    out.value += `[${time}] ${text.trim()}\n`
  }
}

n.on('ready', () => {
  log('network ready')
  const code = prompt('Lobby code? (empty to create a new one)')
  if (code == null || code === '') {
    n.create()
  } else {
    n.join(code)
  }

  n.on('message', (peer, channel, data) => {
    if (channel === 'reliable') {
      log(`${peer.id} said "${data as string}" via ${channel}`)
    }
  })
  inp.addEventListener('keyup', e => {
    if (e.key === 'Enter') {
      log(`sending ${inp.value}`)
      n.broadcast('reliable', inp.value)
      inp.value = ''
    }
  })
})

n.on('lobby', code => {
  log(`lobby code ready: ${code} (and you are ${n.id})`)
})

n.on('signalingerror', console.error.bind(console.error))
n.on('rtcerror', console.error.bind(console.error))

n.on('peerconnected', peer => {
  log(`peer connected: ${peer.id}@${Math.floor(peer.latency * 1000)}ms (${n.size} peers now)`)
  n.broadcast('reliable', `got new peer! ${peer.id}`)
  setInterval(() => {
    n.send('unreliable', peer.id, 'bogus data')
  }, 16)
})
