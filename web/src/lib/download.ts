export function saveFile(blob: Blob, name: string): void {
  const link = document.createElement('a')
  link.href = URL.createObjectURL(blob)
  link.download = name
  link.click()
  // Some engines read the blob only after the click has returned.
  window.setTimeout(() => URL.revokeObjectURL(link.href), 60_000)
}
