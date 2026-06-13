package ui

// GetScripts returns the JavaScript for the debug interface
func GetScripts() string {
	return `
        // Auto-refresh every 30 seconds
        setTimeout(function() {
            location.reload();
        }, 30000);

        document.addEventListener('DOMContentLoaded', function() {
            // Expandable content
            document.querySelectorAll('.expandable-content').forEach(function(element) {
                element.addEventListener('click', function(e) {
                    e.stopPropagation();
                    this.classList.toggle('expanded');
                });
            });

            // Mobile sidebar toggle
            var app = document.getElementById('app');
            var toggle = document.getElementById('sidebar-toggle');
            var backdrop = document.getElementById('sidebar-backdrop');
            if (toggle && app) {
                toggle.addEventListener('click', function() { app.classList.toggle('sidebar-open'); });
            }
            if (backdrop && app) {
                backdrop.addEventListener('click', function() { app.classList.remove('sidebar-open'); });
            }

            // Theme toggle (shares the 'agentize-theme' key with the docs UI)
            var themeBtn = document.getElementById('theme-toggle');
            if (themeBtn) {
                themeBtn.addEventListener('click', function() {
                    var cur = document.documentElement.getAttribute('data-bs-theme') === 'dark' ? 'light' : 'dark';
                    document.documentElement.setAttribute('data-bs-theme', cur);
                    try { localStorage.setItem('agentize-theme', cur); } catch (e) {}
                });
            }
        });
    `
}

// GetBootstrapJS returns the Bootstrap JavaScript CDN URL
func GetBootstrapJS() string {
	return `https://cdn.jsdelivr.net/npm/bootstrap@5.3.2/dist/js/bootstrap.bundle.min.js`
}

// GetBootstrapJSIntegrity returns the integrity hash for Bootstrap JS
func GetBootstrapJSIntegrity() string {
	return `sha384-BBtl+eGJRgqQAUMxJ7pMwbEyER4l1g+O15P+16Ep7Q9Q+zqX6gSbd85u4mG4QzX+`
}
