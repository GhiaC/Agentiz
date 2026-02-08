package ui

// GetStyles returns the CSS styles for the debug interface
// This can be easily moved to an external .css file in the future
func GetStyles() string {
	return `
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
        }
        .main-container {
            background: white;
            border-radius: 15px;
            box-shadow: 0 10px 40px rgba(0,0,0,0.1);
            padding: 2rem;
            margin: 2rem 0;
        }
        .card {
            border: none;
            border-radius: 10px;
            box-shadow: 0 2px 10px rgba(0,0,0,0.08);
            transition: transform 0.2s, box-shadow 0.2s;
        }
        .card:hover {
            transform: translateY(-2px);
            box-shadow: 0 4px 20px rgba(0,0,0,0.12);
        }
        .card-header {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            border-radius: 10px 10px 0 0 !important;
            font-weight: 600;
        }
        .table {
            border-radius: 8px;
            overflow: hidden;
        }
        .table thead {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
        }
        .table tbody tr {
            transition: background-color 0.2s;
        }
        .table tbody tr:hover {
            background-color: #f8f9fa;
        }
        .badge {
            padding: 0.4em 0.8em;
            font-weight: 500;
        }
        .text-justify {
            text-align: justify;
        }
        code {
            background-color: #f4f4f4;
            padding: 0.2em 0.4em;
            border-radius: 4px;
            font-size: 0.9em;
        }
        pre {
            background-color: #f8f9fa;
            padding: 1rem;
            border-radius: 6px;
            border: 1px solid #e9ecef;
        }
        .expandable-content {
            cursor: pointer;
            position: relative;
        }
        .expandable-content:hover {
            background-color: #f8f9fa;
            border-radius: 4px;
            padding: 2px 4px;
            margin: -2px -4px;
        }
        .expandable-content .expand-icon {
            color: #667eea;
            font-weight: bold;
            margin-left: 4px;
        }
        .expandable-content.expanded .expand-icon::before {
            content: "▼";
        }
        .expandable-content:not(.expanded) .expand-icon::before {
            content: "▶";
        }
        .full-content {
            display: none;
            margin-top: 8px;
            padding: 8px;
            background-color: #f8f9fa;
            border-radius: 4px;
            border-left: 3px solid #667eea;
        }
        .expandable-content.expanded .full-content {
            display: block;
        }
        .stat-card {
            text-align: center;
            height: 100%;
        }
        .stat-card .stat-value {
            font-size: 2.5rem;
            font-weight: bold;
            margin-bottom: 0.5rem;
        }
        .stat-card .stat-label {
            font-size: 1.1rem;
            margin-bottom: 0.5rem;
        }
        .navbar-brand {
            font-weight: bold;
        }
        .config-table td {
            vertical-align: middle;
        }
        .config-value {
            font-family: monospace;
            background-color: #f8f9fa;
            padding: 0.25rem 0.5rem;
            border-radius: 4px;
        }
    `
}

// GetNavbarStyles returns specific styles for the navbar
func GetNavbarStyles() string {
	return `
        .navbar {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            box-shadow: 0 2px 10px rgba(0,0,0,0.1);
        }
    `
}
