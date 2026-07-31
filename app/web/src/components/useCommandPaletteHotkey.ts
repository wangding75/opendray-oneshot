import { useEffect } from 'react'

/**
 * Hook that wires ⌘K / Ctrl K to a CommandPalette open-state setter.
 * Call from AppShell (so palette only mounts inside protected area).
 */
export function useCommandPaletteHotkey(setOpen: (next: (v: boolean) => boolean) => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
        e.preventDefault()
        setOpen((v) => !v)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [setOpen])
}
