// preload.ts is thin wiring (Electron import only here, keeping to the
// electron-free-testable-logic split) — it exposes a narrow bridge
// to the renderer, never the raw ipcRenderer/node globals (contextIsolation).
import { contextBridge, ipcRenderer } from 'electron';

contextBridge.exposeInMainWorld('kit', {
  getSession: () => ipcRenderer.invoke('kit:session'),
  restart: () => ipcRenderer.invoke('kit:restart'),
  openExternal: (url: string) => ipcRenderer.invoke('kit:open-external', url),
});
