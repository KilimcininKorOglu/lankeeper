(function () {
    'use strict';

    // Live dashboard tiles fed by the /events/stats SSE channel.
    //
    // In a file rather than an inline <script> because the policy the
    // server sends is `script-src 'self'` with no 'unsafe-inline', which
    // blocks an inline block outright. The dashboard simply stopped
    // updating, with no error the operator could see.

    var canvas = document.getElementById('bandwidth-canvas');
    if (!canvas || !window.BandwidthChart) return;

    var chart = window.BandwidthChart('bandwidth-canvas', 60);
    if (!chart) return;

    function setStat(id, percent) {
        var el = document.getElementById(id);
        if (!el || percent === undefined) return;
        // textContent plus a separate element for the unit, rather than
        // innerHTML with markup in the string. The values come from the
        // router's own SSE payload, but keeping every DOM write off the
        // HTML parser is the rule the rest of the UI follows.
        el.textContent = Math.round(percent);
        var unit = document.createElement('span');
        unit.className = 'stat-unit';
        unit.textContent = '%';
        el.appendChild(unit);
    }

    var source = new EventSource('/events/stats');
    source.addEventListener('stats', function (evt) {
        var data;
        try {
            data = JSON.parse(evt.data);
        } catch (err) {
            return;
        }

        setStat('stat-cpu', data.CPUPercent);
        setStat('stat-ram', data.RAMPercent);

        if (!data.Interfaces) return;
        var rx = 0, tx = 0;
        for (var name in data.Interfaces) {
            if (!Object.prototype.hasOwnProperty.call(data.Interfaces, name)) continue;
            rx += data.Interfaces[name].RxBytesPerSec || 0;
            tx += data.Interfaces[name].TxBytesPerSec || 0;
        }
        chart.addPoint(rx, tx);
    });
})();
