/// <reference types="vite/client" />

import { Terminal } from "./terminal";

const app = document.getElementById("app");
if (!app) {
  throw new Error("root element #app not found");
}

const terminal = new Terminal();
terminal.mount(document.getElementById("terminal")!);
terminal.write("Welcome to nocx\n");
terminal.write("Terminal engine: ghostty-web (mounting...)\n");

// Expose for dev / Wails binding
(window as unknown as Record<string, unknown>).__nocx = { terminal };
