import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  horizontalListSortingStrategy,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { cn } from "@braedonsaunders/appkit-ui";
import { GripVertical } from "lucide-react";
import type { CSSProperties, HTMLAttributes, ReactNode } from "react";

export interface DragHandleProps {
  attributes: HTMLAttributes<HTMLElement>;
  listeners: Record<string, unknown> | undefined;
  isDragging: boolean;
}

/**
 * Drag-to-reorder list (pointer, touch and keyboard: focus a handle, press
 * Space, move with the arrow keys, Space again to drop).
 */
export function SortableList<T>({
  items,
  getId,
  onReorder,
  renderItem,
  axis = "vertical",
  className,
}: {
  items: T[];
  getId: (item: T) => string;
  onReorder: (next: T[], moved: { from: number; to: number }) => void;
  renderItem: (item: T, handle: DragHandleProps, index: number) => ReactNode;
  axis?: "vertical" | "horizontal";
  className?: string;
}) {
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const ids = items.map(getId);
  const onDragEnd = (e: DragEndEvent) => {
    if (!e.over || e.active.id === e.over.id) return;
    const from = ids.indexOf(String(e.active.id));
    const to = ids.indexOf(String(e.over.id));
    if (from < 0 || to < 0) return;
    onReorder(arrayMove(items, from, to), { from, to });
  };
  return (
    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
      <SortableContext
        items={ids}
        strategy={axis === "vertical" ? verticalListSortingStrategy : horizontalListSortingStrategy}
      >
        <div className={className}>
          {items.map((item, i) => (
            <SortableItem key={ids[i]} id={ids[i] as string}>
              {(handle) => renderItem(item, handle, i)}
            </SortableItem>
          ))}
        </div>
      </SortableContext>
    </DndContext>
  );
}

function SortableItem({
  id,
  children,
}: {
  id: string;
  children: (h: DragHandleProps) => ReactNode;
}) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id,
  });
  const style: CSSProperties = {
    transform: CSS.Transform.toString(transform),
    transition,
    zIndex: isDragging ? 20 : undefined,
    position: "relative",
  };
  return (
    <div ref={setNodeRef} style={style} className={cn(isDragging && "opacity-80")}>
      {children({
        attributes: attributes as unknown as HTMLAttributes<HTMLElement>,
        listeners,
        isDragging,
      })}
    </div>
  );
}

export function DragHandle({
  handle,
  label,
  className,
}: {
  handle: DragHandleProps;
  label: string;
  className?: string;
}) {
  return (
    <button
      type="button"
      {...handle.attributes}
      {...(handle.listeners as HTMLAttributes<HTMLButtonElement>)}
      aria-label={label}
      className={cn(
        "grid size-8 shrink-0 cursor-grab touch-none place-items-center rounded-md text-fg-subtle hover:bg-surface-hover hover:text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 active:cursor-grabbing",
        className,
      )}
    >
      <GripVertical size={16} />
    </button>
  );
}

export { arrayMove };
