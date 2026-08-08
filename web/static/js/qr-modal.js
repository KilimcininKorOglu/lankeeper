(function () {
    'use strict';

    // The config is fetched, drawn and dropped. It is never assigned to
    // a variable that outlives the draw, never put in a data attribute
    // and never passed through innerHTML, because the payload is a
    // private key. A canvas is the whole point: it is not an HTML sink.
    var overlay, canvas, titleEl, noteEl, inflight;

    function refs() {
        if (overlay) return true;
        overlay = document.getElementById('qr-modal');
        if (!overlay) return false;
        canvas = document.getElementById('qr-canvas');
        titleEl = document.getElementById('qr-modal-title');
        noteEl = document.getElementById('qr-modal-note');
        return true;
    }

    function msg(key) {
        return (overlay && overlay.dataset[key]) || key;
    }

    function clear() {
        if (!canvas) return;
        var ctx = canvas.getContext('2d');
        ctx.clearRect(0, 0, canvas.width, canvas.height);
        // Zero the backing store too. clearRect leaves the old pixels
        // readable at the previous dimensions on some engines.
        canvas.width = 1;
        canvas.height = 1;
    }

    function close() {
        if (!overlay) return;
        overlay.hidden = true;
        clear();
        noteEl.textContent = '';
    }

    function open(url, label) {
        if (!refs()) return;
        titleEl.textContent = label || msg('title');
        noteEl.textContent = msg('loading');
        clear();
        overlay.hidden = false;

        // One request at a time: a second click while the first is in
        // flight would race two draws onto the same canvas.
        if (inflight) inflight.abort();
        var ctl = new AbortController();
        inflight = ctl;

        fetch(url, { credentials: 'same-origin', signal: ctl.signal })
            .then(function (res) {
                if (!res.ok) throw new Error('http ' + res.status);
                return res.text();
            })
            .then(function (text) {
                try {
                    window.QRCode.render(canvas, text, { level: 'L', scale: 4 });
                    noteEl.textContent = '';
                } catch (err) {
                    clear();
                    noteEl.textContent = msg('tooLarge');
                }
                text = null;
            })
            .catch(function (err) {
                if (err.name === 'AbortError') return;
                clear();
                noteEl.textContent = msg('failed');
            })
            .finally(function () {
                if (inflight === ctl) inflight = null;
            });
    }

    document.addEventListener('click', function (evt) {
        var trigger = evt.target.closest('[data-qr-url]');
        if (trigger) {
            evt.preventDefault();
            open(trigger.dataset.qrUrl, trigger.dataset.qrLabel);
            return;
        }
        if (evt.target.closest('[data-qr-close]') ||
            (overlay && !overlay.hidden && evt.target === overlay)) {
            close();
        }
    });

    document.addEventListener('keydown', function (evt) {
        if (evt.key === 'Escape' && overlay && !overlay.hidden) close();
    });
})();
