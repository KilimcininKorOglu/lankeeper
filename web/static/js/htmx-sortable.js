/* Minimal native HTML5 drag-and-drop sortable.
 *
 * Activate by adding `data-sortable` to a container element. Each
 * direct child becomes a draggable row keyed by its `data-name`
 * attribute. On drop the container POSTs a JSON array of the new
 * `data-name` order to the URL given by `data-reorder-url`
 * (required).
 *
 * Optional attributes on the container:
 *   data-pin-first  — when present, the first child cannot be moved
 *                     and other children cannot be dropped before it.
 *                     Used by the IPv6 Announced table where the
 *                     primary LAN bridge keeps SLA-ID 0 by contract.
 *
 * The implementation is intentionally tiny — no external Sortable.js
 * dependency, no animation, no touch gestures. Good enough for the
 * handful of admin reorder surfaces in this app. */
(function() {
    function rowsOf(container) {
        return Array.from(container.children).filter(function(c) {
            return c.nodeType === 1 && c.dataset && c.dataset.name;
        });
    }

    function pinFirst(container) {
        return container.dataset.pinFirst !== undefined;
    }

    function postOrder(container) {
        var url = container.dataset.reorderUrl;
        if (!url) return;
        var order = rowsOf(container).map(function(r) { return r.dataset.name; });
        fetch(url, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(order)
        });
    }

    function init(container) {
        if (container._sortableInit) return;
        container._sortableInit = true;

        var rows = rowsOf(container);
        rows.forEach(function(item, idx) {
            var locked = pinFirst(container) && idx === 0;
            if (locked) {
                item.draggable = false;
                item.style.cursor = 'default';
                return;
            }
            item.draggable = true;
            item.style.cursor = 'grab';

            item.addEventListener('dragstart', function(e) {
                e.dataTransfer.setData('text/plain', item.dataset.name || '');
                item.style.opacity = '0.5';
            });
            item.addEventListener('dragend', function() {
                item.style.opacity = '1';
            });
            item.addEventListener('dragover', function(e) {
                e.preventDefault();
            });
            item.addEventListener('drop', function(e) {
                e.preventDefault();
                var draggedName = e.dataTransfer.getData('text/plain');
                if (!draggedName || draggedName === item.dataset.name) return;

                var siblings = rowsOf(container);
                var moved = siblings.find(function(s) { return s.dataset.name === draggedName; });
                if (!moved) return;

                var fromIdx = siblings.indexOf(moved);
                var toIdx = siblings.indexOf(item);
                if (pinFirst(container) && toIdx === 0) {
                    // Refuse to land before a pinned first row.
                    return;
                }
                if (fromIdx === toIdx) return;

                if (fromIdx < toIdx) {
                    container.insertBefore(moved, item.nextSibling);
                } else {
                    container.insertBefore(moved, item);
                }
                postOrder(container);
            });
        });
    }

    function scan() {
        document.querySelectorAll('[data-sortable]').forEach(init);
    }

    document.addEventListener('DOMContentLoaded', scan);
    document.addEventListener('htmx:afterSettle', scan);
})();
