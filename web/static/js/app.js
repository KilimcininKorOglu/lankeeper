(function() {
    // The theme lives in a cookie. Browser storage held a second copy
    // that was read first, which was one place for the two to disagree
    // and nothing this setting needed.
    var saved = getCookie('theme');
    if (!saved) {
        saved = window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
    }
    document.documentElement.setAttribute('data-theme', saved);

    window.toggleTheme = function() {
        var current = document.documentElement.getAttribute('data-theme');
        var next = current === 'dark' ? 'light' : 'dark';
        document.documentElement.setAttribute('data-theme', next);
        document.cookie = 'theme=' + next + '; path=/; max-age=31536000; SameSite=Strict; Secure';
    };

    window.showToast = function(message, type) {
        var container = document.getElementById('toast-container');
        if (!container) return;
        var toast = document.createElement('div');
        toast.className = 'toast' + (type ? ' toast-' + type : '');
        toast.textContent = message;
        container.appendChild(toast);
        setTimeout(function() {
            toast.style.opacity = '0';
            toast.style.transform = 'translateY(16px)';
            toast.style.transition = 'all 0.3s ease';
            setTimeout(function() { toast.remove(); }, 300);
        }, 3000);
    };

    // Echo the CSRF token into every htmx request.
    //
    // The server accepts either the X-CSRF-Token header or a csrf_token
    // form field, and only the login and logout forms carry the field.
    // Every other mutating control in the UI is an hx-post on a bare
    // button, which sends neither, so the whole mutating surface was
    // answered 403. The cookie is deliberately not HttpOnly for exactly
    // this reason.
    //
    // Safe methods are skipped: the server issues the token on GET, and
    // sending it back on a request that does not need it only widens
    // where the value travels.
    document.addEventListener('htmx:configRequest', function(evt) {
        var verb = (evt.detail.verb || '').toLowerCase();
        if (verb === 'get' || verb === 'head') return;
        var token = getCookie('csrf_token');
        if (token) evt.detail.headers['X-CSRF-Token'] = token;
    });

    // Exposed because every fetch() that mutates state needs the same
    // token htmx gets above, and reading the cookie in a second place
    // would be a second thing to keep in step with the server.
    window.lankeeperCSRFToken = function() {
        return getCookie('csrf_token');
    };

    function getCookie(name) {
        var match = document.cookie.match(new RegExp('(^| )' + name + '=([^;]+)'));
        return match ? match[2] : null;
    }
})();
