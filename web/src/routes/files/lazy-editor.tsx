import { lazyChunk } from '@/components/lazy-chunk'

export const FileEditor = lazyChunk(
  () => import('@/routes/files/editor').then((m) => m.FileEditor),
  <p className="flex min-h-0 min-w-0 flex-1 items-center justify-center bg-canvas p-4 text-ui text-muted">Loading file…</p>,
)
