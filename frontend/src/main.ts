import { mount } from 'svelte'
import './app.css'
import App from './App.svelte'
import { LogMessage } from '../wailsjs/go/main/App'

// Forward browser console to Go stdout.
const origLog = console.log;
const origWarn = console.warn;
const origError = console.error;
const origDebug = console.debug;

function forward(level: string, orig: (...args: any[]) => void, ...args: any[]) {
  orig(...args);
  try {
    LogMessage(level, args.map(a => typeof a === 'object' ? JSON.stringify(a) : String(a)).join(' '));
  } catch {}
}

console.log = (...args) => forward('log', origLog, ...args);
console.warn = (...args) => forward('warn', origWarn, ...args);
console.error = (...args) => forward('error', origError, ...args);
console.debug = (...args) => forward('debug', origDebug, ...args);

const app = mount(App, {
  target: document.getElementById('app')!,
})

export default app
