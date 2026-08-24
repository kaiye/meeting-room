import { X } from 'lucide-react'
import { type ReactNode } from 'react'

export default function Modal({
  title,
  children,
  footer,
  onClose,
  wide,
}: {
  title: string
  children: ReactNode
  footer?: ReactNode
  onClose: () => void
  wide?: boolean
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40" onMouseDown={onClose}>
      <div
        className={`mx-4 flex max-h-[85vh] w-full ${wide ? 'max-w-2xl' : 'max-w-md'} flex-col rounded-lg bg-white p-6 shadow-xl`}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="mb-4 flex shrink-0 items-center justify-between">
          <h2 className="text-lg font-semibold">{title}</h2>
          <button onClick={onClose} className="rounded p-1 text-gray-400 hover:bg-gray-100 hover:text-gray-600">
            <X size={18} />
          </button>
        </div>
        <div className="custom-scrollbar min-h-0 flex-1 overflow-y-auto">{children}</div>
        {footer && <div className="mt-6 flex shrink-0 justify-end gap-2">{footer}</div>}
      </div>
    </div>
  )
}
