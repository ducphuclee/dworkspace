import { useLayoutEffect, useRef, type ReactNode } from 'react';

/** A `.menu` that the list it opens from cannot cut off.
 *
 *  The sidebar's tree scrolls, and a scroller clips whatever hangs out of it:
 *  the ⋯ menu of a row near its bottom vanished under the Templates section
 *  below (#21). Moving the menu into a portal would free it, but it would also
 *  take it out of the row's DOM, where the outside-click handling looks for it
 *  (useMenuDismiss). So it stays where it is and only changes how it is
 *  positioned: fixed, at exactly the place the stylesheet put it, which no
 *  scroller clips. Where that place runs off the screen it opens above its row
 *  instead, and it follows the row while the list scrolls. */
export default function AnchoredMenu({ className = 'menu', children }: { className?: string; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const menu = ref.current;
    const anchor = menu?.parentElement;
    if (!menu || !anchor) return;
    // Where CSS put it, relative to its anchor. Kept, so it opens exactly
    // where it always did.
    const laid = menu.getBoundingClientRect();
    const first = anchor.getBoundingClientRect();
    const dx = laid.left - first.left;
    const dy = laid.top - first.top;
    const place = () => {
      const a = anchor.getBoundingClientRect();
      menu.style.position = 'fixed';
      menu.style.right = 'auto';
      menu.style.bottom = 'auto';
      menu.style.top = '0px';
      menu.style.left = '0px';
      // Where (0, 0) really is: on a phone the drawer is moved by a transform,
      // and a transformed ancestor becomes the origin of `fixed`.
      const origin = menu.getBoundingClientRect();
      let top = a.top + dy;
      if (top + origin.height > window.innerHeight - 8) top = Math.max(8, a.top - origin.height - 4);
      const left = Math.max(8, Math.min(a.left + dx, window.innerWidth - origin.width - 8));
      menu.style.top = `${top - origin.top}px`;
      menu.style.left = `${left - origin.left}px`;
      // A long menu (every workspace to move to) still fits: it scrolls.
      menu.style.maxHeight = `${window.innerHeight - 16}px`;
      menu.style.overflowY = 'auto';
    };
    place();
    window.addEventListener('scroll', place, true);
    window.addEventListener('resize', place);
    return () => {
      window.removeEventListener('scroll', place, true);
      window.removeEventListener('resize', place);
    };
  }, []);
  return (
    <div ref={ref} className={className}>
      {children}
    </div>
  );
}
