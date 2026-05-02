(function() {
    var saved = localStorage.getItem('theme') || getCookie('theme');
    if (!saved) {
        saved = window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
    }
    document.documentElement.setAttribute('data-theme', saved);

    window.toggleTheme = function() {
        var current = document.documentElement.getAttribute('data-theme');
        var next = current === 'dark' ? 'light' : 'dark';
        document.documentElement.setAttribute('data-theme', next);
        localStorage.setItem('theme', next);
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

    function getCookie(name) {
        var match = document.cookie.match(new RegExp('(^| )' + name + '=([^;]+)'));
        return match ? match[2] : null;
    }
})();
