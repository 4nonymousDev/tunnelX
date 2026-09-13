/// <reference types="vite/client" />

import type { TunnelXDesktopAPI } from '../shared/ipc'

declare global {
  interface Window {
    tunnelx?: TunnelXDesktopAPI
  }
}

export {}
