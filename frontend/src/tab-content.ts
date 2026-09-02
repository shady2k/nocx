export interface TabHost {
  setTitle(title: string): void
  updateTooltip?(tooltip: string): void
  requestAttention(): void
  requestClose(): void
}
