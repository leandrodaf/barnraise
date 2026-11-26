import { mount } from 'svelte'
import './app.css'
import App from './App.svelte'
import { watchStaleBuild } from './lib/build'
import { routeExternalLinks } from './lib/external'

watchStaleBuild()
routeExternalLinks()

const app = mount(App, {
  target: document.getElementById('app')!,
})

export default app
