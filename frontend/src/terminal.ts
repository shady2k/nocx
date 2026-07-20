export class Terminal {
  private element: HTMLElement | null = null;

  mount(container: HTMLElement): void {
    this.element = document.createElement("pre");
    this.element.style.fontFamily =
      '"SF Mono", "Fira Code", "Cascadia Code", monospace';
    this.element.style.background = "#1a1b26";
    this.element.style.color = "#c0caf5";
    this.element.style.padding = "12px";
    this.element.style.margin = "0";
    this.element.style.overflow = "auto";
    this.element.style.height = "100%";
    this.element.style.whiteSpace = "pre-wrap";
    this.element.style.wordBreak = "break-all";
    this.element.textContent = "";
    container.appendChild(this.element);
  }

  write(text: string): void {
    if (!this.element) return;
    this.element.textContent += text;
  }

  clear(): void {
    if (!this.element) return;
    this.element.textContent = "";
  }
}
