package ui

// GetScripts returns the JavaScript for the debug interface
func GetScripts() string {
	return `
        // Auto-refresh every 30 seconds
        setTimeout(function() {
            location.reload();
        }, 30000);
        
        // Expandable content functionality
        document.addEventListener('DOMContentLoaded', function() {
            document.querySelectorAll('.expandable-content').forEach(function(element) {
                element.addEventListener('click', function(e) {
                    e.stopPropagation();
                    this.classList.toggle('expanded');
                });
            });
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
