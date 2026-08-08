// vpn-s2s.js — wizard glue. POSTs go through fetch() rather than
// htmx because the responses are JSON we need to splice into hidden
// form fields, not HTML fragments to swap.
(function () {
    'use strict';

    var ns = {};

    function el(id) { return document.getElementById(id); }

    // The server accepts the CSRF token as a header or a form field, and
    // only the login and logout forms carry the field. Every POST here is
    // a bare fetch, so without this header the whole wizard is answered
    // 403 with no hint as to why.
    function withCSRF(headers) {
        headers = headers || {};
        var token = window.lankeeperCSRFToken && window.lankeeperCSRFToken();
        if (token) headers['X-CSRF-Token'] = token;
        return headers;
    }

    function postForm(url, form) {
        var body = new URLSearchParams();
        new FormData(form).forEach(function (v, k) { body.append(k, v); });
        return fetch(url, {
            method: 'POST',
            headers: withCSRF({ 'Accept': 'application/json' }),
            body: body,
            credentials: 'same-origin',
        }).then(function (res) {
            if (!res.ok) {
                return res.text().then(function (msg) { throw new Error(msg || ('HTTP ' + res.status)); });
            }
            return res.json();
        });
    }

    function fmtExpires(iso) {
        if (!iso) return '';
        try {
            var d = new Date(iso);
            return d.toLocaleString();
        } catch (e) {
            return iso;
        }
    }

    function bindIssue() {
        var f = el('s2s-issue-form');
        if (!f) return;
        f.addEventListener('submit', function (ev) {
            ev.preventDefault();
            postForm('/vpn/s2s/invite', f).then(function (data) {
                el('s2s-invite-token').value = data.token || '';
                el('s2s-finalize-peer').value = data.peerName || '';
                el('s2s-token-expires').textContent = data.expiresAt
                    ? ' (expires ' + fmtExpires(data.expiresAt) + ')'
                    : '';
                el('s2s-invite-result').style.display = 'block';
            }).catch(function (err) {
                alert(err.message);
            });
        });
    }

    function bindFinalize() {
        var f = el('s2s-finalize-form');
        if (!f) return;
        f.addEventListener('submit', function (ev) {
            ev.preventDefault();
            postForm('/vpn/s2s/finalize', f).then(function () {
                location.reload();
            }).catch(function (err) {
                alert(err.message);
            });
        });
    }

    function bindJoin() {
        var f = el('s2s-join-form');
        if (!f) return;
        f.addEventListener('submit', function (ev) {
            ev.preventDefault();
            postForm('/vpn/s2s/join', f).then(function (data) {
                el('s2s-ack-token').value = data.ackToken || '';
                el('s2s-join-result').style.display = 'block';
            }).catch(function (err) {
                alert(err.message);
            });
        });
    }

    ns.copyToken = function () {
        var ta = el('s2s-invite-token');
        ta.select();
        try { document.execCommand('copy'); } catch (e) { /* ignore */ }
        if (navigator.clipboard) {
            navigator.clipboard.writeText(ta.value).catch(function () { /* ignore */ });
        }
    };

    ns.copyAck = function () {
        var ta = el('s2s-ack-token');
        ta.select();
        try { document.execCommand('copy'); } catch (e) { /* ignore */ }
        if (navigator.clipboard) {
            navigator.clipboard.writeText(ta.value).catch(function () { /* ignore */ });
        }
    };

    ns.testReachability = function (name) {
        fetch('/vpn/s2s/' + encodeURIComponent(name) + '/reachability', {
            method: 'POST',
            headers: withCSRF(),
            credentials: 'same-origin',
        }).then(function (res) {
            if (res.status === 204) {
                alert('Reachable.');
            } else {
                return res.text().then(function (msg) { alert('Unreachable: ' + (msg || res.status)); });
            }
        }).catch(function (err) {
            alert(err.message);
        });
    };

    // Delegated rather than inline onclick attributes: the policy the
    // server sends carries no 'unsafe-inline', so an onclick never runs.
    function bindActions() {
        document.addEventListener('click', function (evt) {
            var el = evt.target.closest('[data-s2s-action]');
            if (!el) return;
            switch (el.dataset.s2sAction) {
            case 'copy-token':
                ns.copyToken();
                break;
            case 'copy-ack':
                ns.copyAck();
                break;
            case 'test-reachability':
                ns.testReachability(el.dataset.s2sPeer);
                break;
            }
        });
    }

    function init() {
        bindIssue();
        bindFinalize();
        bindJoin();
        bindActions();
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }

    window.vpnS2S = ns;
})();
