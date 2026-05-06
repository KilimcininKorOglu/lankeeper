// qos-chart.js — per-client bandwidth live table + sparklines.
// Subscribes to /events/qos (SSE event "qos-clients") and renders a
// stacked tbody plus a 60-sample sparkline canvas per MAC. Uses no
// external charting libraries; the project ships zero JS deps.
(function () {
    'use strict';

    var TBODY_ID = 'qos-clients-tbody';
    var MAX_POINTS = 60;

    var sparklines = Object.create(null); // mac -> { canvas, in: [], out: [] }

    function fmtBps(bps) {
        if (bps == null || isNaN(bps)) return '0 b/s';
        var v = Number(bps);
        if (v < 1000) return v.toFixed(0) + ' b/s';
        if (v < 1e6) return (v / 1e3).toFixed(1) + ' kb/s';
        if (v < 1e9) return (v / 1e6).toFixed(2) + ' Mb/s';
        return (v / 1e9).toFixed(2) + ' Gb/s';
    }

    function fmtBytes(b) {
        if (b == null || isNaN(b)) return '0 B';
        var v = Number(b);
        if (v < 1024) return v.toFixed(0) + ' B';
        if (v < 1024 * 1024) return (v / 1024).toFixed(1) + ' KiB';
        if (v < 1024 * 1024 * 1024) return (v / (1024 * 1024)).toFixed(2) + ' MiB';
        return (v / (1024 * 1024 * 1024)).toFixed(2) + ' GiB';
    }

    function ensureSparkline(mac) {
        var s = sparklines[mac];
        if (s) return s;
        var canvas = document.createElement('canvas');
        canvas.width = 120;
        canvas.height = 28;
        canvas.style.width = '120px';
        canvas.style.height = '28px';
        canvas.dataset.mac = mac;
        s = { canvas: canvas, in: [], out: [] };
        sparklines[mac] = s;
        return s;
    }

    function pushSample(mac, inBps, outBps) {
        var s = ensureSparkline(mac);
        s.in.push(Number(inBps) || 0);
        s.out.push(Number(outBps) || 0);
        if (s.in.length > MAX_POINTS) s.in.splice(0, s.in.length - MAX_POINTS);
        if (s.out.length > MAX_POINTS) s.out.splice(0, s.out.length - MAX_POINTS);
        drawSpark(s);
    }

    function drawSpark(s) {
        var ctx = s.canvas.getContext('2d');
        var w = s.canvas.width;
        var h = s.canvas.height;
        ctx.clearRect(0, 0, w, h);

        var all = s.in.concat(s.out);
        var maxVal = 1;
        for (var i = 0; i < all.length; i++) {
            if (all[i] > maxVal) maxVal = all[i];
        }
        var pad = 2;
        drawLine(ctx, s.in, w, h, pad, maxVal, getCSSVar('--accent-blue', '#1D9BF0'));
        drawLine(ctx, s.out, w, h, pad, maxVal, getCSSVar('--accent-green', '#00BA7C'));
    }

    function drawLine(ctx, data, w, h, pad, maxVal, color) {
        if (data.length < 2) return;
        ctx.beginPath();
        ctx.strokeStyle = color;
        ctx.lineWidth = 1.5;
        ctx.lineJoin = 'round';
        var step = (w - pad * 2) / (MAX_POINTS - 1);
        var startIdx = Math.max(0, data.length - MAX_POINTS);
        for (var i = startIdx; i < data.length; i++) {
            var x = pad + (i - startIdx) * step;
            var y = h - pad - ((data[i] / maxVal) * (h - pad * 2));
            if (i === startIdx) ctx.moveTo(x, y);
            else ctx.lineTo(x, y);
        }
        ctx.stroke();
    }

    function getCSSVar(name, fallback) {
        try {
            var v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
            return v || fallback;
        } catch (e) {
            return fallback;
        }
    }

    function render(clients) {
        var tbody = document.getElementById(TBODY_ID);
        if (!tbody) return;
        if (!clients || !clients.length) {
            tbody.innerHTML = '<tr><td colspan="5" style="color:var(--text-secondary);">' +
                (window.qosI18n && window.qosI18n.empty ? window.qosI18n.empty : '') + '</td></tr>';
            return;
        }

        // Push samples first so sparklines accumulate continuously.
        var seen = Object.create(null);
        for (var i = 0; i < clients.length; i++) {
            var c = clients[i];
            seen[c.mac] = true;
            pushSample(c.mac, c.inBps, c.outBps);
        }
        // Drop sparklines for vanished MACs.
        for (var k in sparklines) {
            if (!seen[k]) delete sparklines[k];
        }

        // Rebuild rows. Hostname column falls back to "?" when the
        // lease has no DHCP-supplied name.
        var rows = '';
        for (var j = 0; j < clients.length; j++) {
            var cl = clients[j];
            var host = cl.hostname || '?';
            var ip = cl.ip ? '<div style="color:var(--text-secondary); font-size:var(--font-xs);">' + escapeHtml(cl.ip) + '</div>' : '';
            rows += '<tr data-mac="' + escapeHtml(cl.mac) + '">' +
                '<td><div>' + escapeHtml(host) + '</div>' + ip + '</td>' +
                '<td style="font-family:var(--font-mono);">' + escapeHtml(cl.mac) + '</td>' +
                '<td style="text-align:right;">' + fmtBps(cl.inBps) +
                '<div style="color:var(--text-secondary); font-size:var(--font-xs);">' + fmtBytes(cl.inBytes) + '</div></td>' +
                '<td style="text-align:right;">' + fmtBps(cl.outBps) +
                '<div style="color:var(--text-secondary); font-size:var(--font-xs);">' + fmtBytes(cl.outBytes) + '</div></td>' +
                '<td class="qos-spark"></td>' +
                '</tr>';
        }
        tbody.innerHTML = rows;

        // Re-attach sparkline canvases. innerHTML wiped them out.
        var trList = tbody.querySelectorAll('tr[data-mac]');
        for (var t = 0; t < trList.length; t++) {
            var tr = trList[t];
            var mac = tr.dataset.mac;
            var s = sparklines[mac];
            if (!s) continue;
            var cell = tr.querySelector('.qos-spark');
            if (cell) {
                cell.appendChild(s.canvas);
                drawSpark(s);
            }
        }
    }

    function escapeHtml(s) {
        return String(s == null ? '' : s)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#39;');
    }

    function init() {
        if (!('EventSource' in window)) return;
        if (!document.getElementById(TBODY_ID)) return;
        var es = new EventSource('/events/qos');
        es.addEventListener('qos-clients', function (ev) {
            try {
                var data = JSON.parse(ev.data);
                render(data || []);
            } catch (e) {
                // Malformed payloads should never reach the client,
                // but never break the page if they do.
            }
        });
        es.addEventListener('error', function () {
            // EventSource auto-reconnects; nothing to do here.
        });
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }
})();
