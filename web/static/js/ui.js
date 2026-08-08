(function () {
    'use strict';

    // Declarative UI behaviour, driven entirely by data attributes.
    //
    // The server sends `script-src 'self'` with no 'unsafe-inline' and no
    // 'unsafe-eval'. That makes an onclick attribute, an inline <script>
    // block and htmx's hx-on all inert in a browser: the policy blocks
    // them before they run, silently, so the control simply does nothing.
    // Every interaction below used to be written one of those three ways.
    //
    // Listeners are delegated from the document so a fragment htmx swaps
    // in is covered without rebinding.

    function byId(id) {
        return id ? document.getElementById(id) : null;
    }

    function show(el, mode) {
        if (el) el.style.display = mode || 'block';
    }

    function hide(el) {
        if (el) el.style.display = 'none';
    }

    // switchSections shows the one element whose attribute matches value
    // and hides its siblings. Used by the DNS encryption radios and the
    // backup target-type select, which both label their panels with an
    // attribute naming the value they belong to.
    function switchSections(attr, value) {
        document.querySelectorAll('[' + attr + ']').forEach(function (section) {
            section.style.display = section.getAttribute(attr) === value ? '' : 'none';
        });
    }

    document.addEventListener('click', function (evt) {
        var el = evt.target.closest('[data-open], [data-close], [data-close-closest], [data-action], [data-switch]');
        if (!el) return;

        if (el.dataset.open) {
            show(byId(el.dataset.open));
        }
        if (el.dataset.close) {
            hide(byId(el.dataset.close));
        }
        if (el.dataset.closeClosest) {
            hide(el.closest(el.dataset.closeClosest));
        }
        if (el.dataset.action === 'toggle-theme' && window.toggleTheme) {
            window.toggleTheme();
        }
        // A radio reports its choice through value, and its click is the
        // only event that fires when the operator picks an already
        // focused option.
        if (el.dataset.switch) {
            switchSections(el.dataset.switch, el.value);
        }
    });

    document.addEventListener('change', function (evt) {
        var el = evt.target;
        if (!el || !el.dataset) return;

        if (el.dataset.switch && el.tagName === 'SELECT') {
            switchSections(el.dataset.switch, el.value);
        }

        // Reveal a block of extra fields when a select lands on one
        // value. display:contents rather than block, because the fields
        // belong to the parent grid and a block wrapper would break the
        // column layout.
        if (el.dataset.toggle) {
            var target = byId(el.dataset.toggle);
            if (target) {
                target.style.display = el.value === el.dataset.toggleWhen ? 'contents' : 'none';
            }
        }

        // Copy a preset into a text input and lock it, leaving the
        // sentinel value as the way back to typing a custom one.
        if (el.dataset.fill) {
            var input = byId(el.dataset.fill);
            if (input) {
                if (el.value === 'custom') {
                    input.removeAttribute('readonly');
                } else {
                    input.value = el.value;
                    input.setAttribute('readonly', 'readonly');
                }
            }
        }

        if (el.hasAttribute('data-submit-on-change') && el.form) {
            el.form.requestSubmit();
        }
    });

    // htmx's own hx-on attribute is evaluated with new Function, which
    // needs 'unsafe-eval'. This listener does the same job from a file
    // the policy already allows.
    //
    // Gated on success, unlike the attributes it replaces: those reset
    // the form whatever came back, so a rejected submission threw away
    // everything the operator had typed and left no clue why.
    document.addEventListener('htmx:afterRequest', function (evt) {
        var el = evt.target;
        if (!el || !el.dataset || !evt.detail || !evt.detail.successful) return;

        if (el.hasAttribute('data-reset-after-request') && typeof el.reset === 'function') {
            el.reset();
        }
        // A value names the element to hide, so a form that lives
        // inside the panel it opens can close the panel rather than
        // itself. Empty means hide the form.
        if (el.hasAttribute('data-hide-after-request')) {
            var target = el.getAttribute('data-hide-after-request');
            hide(target ? byId(target) : el);
        }
    });
})();
