package ui

// GetStyles returns the CSS for the debug interface.
//
// The debug pages are built on Bootstrap 5.3, so instead of fighting it we
// re-skin Bootstrap with the same design language used by the login page and
// the knowledge-tree docs: a flat surface palette driven by CSS custom
// properties, an accent-blue brand colour, soft "pill" badges, and a left
// sidebar shell. Light/dark are driven by the `data-bs-theme` attribute so
// Bootstrap's own utilities (.text-muted, .bg-light, tables…) flip with us.
func GetStyles() string {
	return `
        /* ============ Design tokens ============ */
        :root, [data-bs-theme="light"] {
            --bg: #f3f5fa; --surface: #ffffff; --surface-2: #f8f9fc; --border: #e4e8f1;
            --text: #1a2233; --text-2: #5b6478; --text-3: #8a93a8;
            --accent: #4f6df5; --accent-rgb: 79,109,245; --accent-soft: rgba(79,109,245,0.10);
            --row-core-bg: rgba(79,109,245,0.06);
            --ok: #15803d; --ok-rgb: 21,128,61; --ok-soft: #e7f6ee;
            --info: #0e7490; --info-rgb: 14,116,144; --info-soft: #e0f2fe;
            --warn: #b45309; --warn-rgb: 180,83,9; --warn-soft: #fdf3e3;
            --danger: #dc2626; --danger-rgb: 220,38,38; --danger-soft: #fdecec;
            --muted-rgb: 138,147,168;
            --code-bg: #f1f3f9;
            --shadow: 0 1px 3px rgba(16,24,40,0.06), 0 1px 2px rgba(16,24,40,0.04);
            --radius: 10px; --sidebar-w: 250px;
        }
        [data-bs-theme="dark"] {
            --bg: #0e1117; --surface: #161b26; --surface-2: #1b2130; --border: #262e40;
            --text: #e6e9f0; --text-2: #9aa3b5; --text-3: #6b7385;
            --accent: #7c93ff; --accent-rgb: 124,147,255; --accent-soft: rgba(124,147,255,0.14);
            --row-core-bg: rgba(124,147,255,0.08);
            --ok: #4ade80; --ok-rgb: 74,222,128; --ok-soft: rgba(74,222,128,0.12);
            --info: #38bdf8; --info-rgb: 56,189,248; --info-soft: rgba(56,189,248,0.12);
            --warn: #fbbf24; --warn-rgb: 251,191,36; --warn-soft: rgba(251,191,36,0.12);
            --danger: #f87171; --danger-rgb: 248,113,113; --danger-soft: rgba(248,113,113,0.12);
            --muted-rgb: 107,115,133;
            --code-bg: #10141d;
            --shadow: 0 1px 2px rgba(0,0,0,0.4);
        }

        /* Map Bootstrap's variables onto our palette so its built-in
           utilities and components adopt the new look in both themes. */
        :root, [data-bs-theme="light"], [data-bs-theme="dark"] {
            --bs-body-bg: var(--bg);
            --bs-body-color: var(--text);
            --bs-emphasis-color: var(--text);
            --bs-heading-color: var(--text);
            --bs-border-color: var(--border);
            --bs-secondary-bg: var(--surface-2);
            --bs-tertiary-bg: var(--surface-2);
            --bs-secondary-color: var(--text-3);
            --bs-tertiary-color: var(--text-3);
            --bs-link-color: var(--accent);
            --bs-link-hover-color: var(--accent);
            --bs-link-color-rgb: var(--accent-rgb);
            --bs-link-hover-color-rgb: var(--accent-rgb);
            --bs-primary: var(--accent);
            --bs-primary-rgb: var(--accent-rgb);
            --bs-success-rgb: var(--ok-rgb);
            --bs-info-rgb: var(--info-rgb);
            --bs-warning-rgb: var(--warn-rgb);
            --bs-danger-rgb: var(--danger-rgb);
            --bs-secondary-rgb: var(--muted-rgb);
            --bs-code-color: var(--accent);
            --bs-border-radius: var(--radius);
        }

        * { box-sizing: border-box; }
        html, body { height: 100%; }
        body {
            margin: 0;
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
            background: var(--bg);
            color: var(--text);
            font-size: 14px;
            line-height: 1.55;
        }
        a { color: var(--accent); text-decoration: none; }
        a:hover { text-decoration: underline; }
        ::placeholder { color: var(--text-3); }
        code, pre, kbd { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
        code {
            background: var(--code-bg); border: 1px solid var(--border);
            border-radius: 5px; padding: 1px 6px; font-size: 0.86em; color: var(--accent);
        }
        pre {
            background: var(--code-bg); border: 1px solid var(--border);
            border-radius: 8px; padding: 12px; overflow-x: auto;
        }
        .text-muted { color: var(--text-3) !important; }
        .text-dark { color: var(--text) !important; }
        [data-bs-theme="dark"] .bg-light { background-color: var(--surface-2) !important; color: var(--text) !important; }
        [data-bs-theme="dark"] .bg-white { background-color: var(--surface) !important; color: var(--text) !important; }
        pre.bg-light { background: var(--code-bg) !important; color: var(--text) !important; }

        /* ============ App shell (sidebar + main) ============ */
        .app { display: flex; height: 100vh; overflow: hidden; }

        .sidebar {
            width: var(--sidebar-w); min-width: var(--sidebar-w);
            background: var(--surface); border-right: 1px solid var(--border);
            display: flex; flex-direction: column;
            transition: transform 0.22s ease;
        }
        .sidebar-brand {
            display: flex; align-items: center; gap: 10px;
            padding: 16px 18px; border-bottom: 1px solid var(--border);
            color: var(--text); text-decoration: none;
        }
        .sidebar-brand:hover { text-decoration: none; }
        .sidebar-brand .brand-mark {
            width: 30px; height: 30px; border-radius: 8px; flex-shrink: 0;
            background: var(--accent); color: #fff;
            display: inline-flex; align-items: center; justify-content: center; font-size: 16px;
        }
        .sidebar-brand .brand-title { font-weight: 700; font-size: 15px; }
        .sidebar-brand .brand-sub { font-size: 11px; color: var(--text-3); }

        .sidebar-nav { flex: 1; overflow-y: auto; padding: 10px 10px 16px; }
        .nav-section-label {
            font-size: 10.5px; font-weight: 700; text-transform: uppercase;
            letter-spacing: 0.07em; color: var(--text-3);
            padding: 14px 10px 5px;
        }
        .nav-section-label:first-child { padding-top: 4px; }
        .nav-link-item {
            display: flex; align-items: center; gap: 10px;
            padding: 8px 10px; border-radius: 8px; margin-bottom: 1px;
            color: var(--text-2); font-size: 13.5px; font-weight: 500;
            text-decoration: none; border-left: 2px solid transparent;
        }
        .nav-link-item:hover { background: var(--surface-2); color: var(--text); text-decoration: none; }
        .nav-link-item.active {
            background: var(--accent-soft); color: var(--accent);
            font-weight: 600; border-left-color: var(--accent);
        }
        .nav-link-item .nav-ico { font-size: 15px; width: 20px; text-align: center; flex-shrink: 0; }

        .sidebar-foot {
            display: flex; gap: 4px; padding: 10px 12px;
            border-top: 1px solid var(--border); font-size: 12px;
        }
        .sidebar-foot a {
            flex: 1; text-align: center; padding: 6px 4px; border-radius: 7px;
            color: var(--text-2); font-weight: 500;
        }
        .sidebar-foot a:hover { background: var(--surface-2); color: var(--accent); text-decoration: none; }

        .sidebar-backdrop {
            display: none; position: fixed; inset: 0; z-index: 25;
            background: rgba(0,0,0,0.4);
        }

        .main { flex: 1; display: flex; flex-direction: column; min-width: 0; }
        .topbar {
            display: flex; align-items: center; gap: 12px;
            padding: 12px 22px; background: var(--surface);
            border-bottom: 1px solid var(--border); flex-shrink: 0;
        }
        .topbar-title { font-size: 16px; font-weight: 700; }
        .topbar-actions { margin-left: auto; display: flex; gap: 8px; }
        .icon-btn {
            border: 1px solid var(--border); background: var(--surface-2);
            border-radius: 8px; width: 34px; height: 34px; cursor: pointer;
            font-size: 15px; color: var(--text-2);
            display: inline-flex; align-items: center; justify-content: center;
        }
        .icon-btn:hover { color: var(--accent); border-color: var(--accent); }
        #sidebar-toggle { display: none; }

        .content-scroll { flex: 1; overflow-y: auto; padding: 24px; }
        .content-scroll .container { max-width: 1180px; padding-left: 0; padding-right: 0; }
        /* Old wrapper kept transparent: individual cards are the surfaces now. */
        .main-container { background: transparent; border-radius: 0; box-shadow: none; padding: 0; margin: 0; }

        /* ============ Cards ============ */
        .card {
            background: var(--surface); border: 1px solid var(--border);
            border-radius: var(--radius); box-shadow: var(--shadow);
        }
        .card-header {
            background: var(--surface-2); color: var(--text);
            border-bottom: 1px solid var(--border); font-weight: 600;
            border-radius: var(--radius) var(--radius) 0 0 !important;
        }
        .card-header h4, .card-header h5, .card-header h6 { color: var(--text); margin: 0; font-size: 15px; }
        .card-header .btn-light { background: var(--surface); }

        /* Stat cards (dashboard) — colored left accent from .border-* */
        .card.text-center.h-100 { transition: border-color 0.12s ease, box-shadow 0.12s ease; }
        a.card:hover, .card.text-center.h-100:hover { border-color: var(--accent); }
        .stat-card { text-align: center; height: 100%; }

        /* ============ Tables ============ */
        .table {
            --bs-table-bg: transparent;
            --bs-table-color: var(--text);
            --bs-table-border-color: var(--border);
            --bs-table-striped-bg: var(--surface-2);
            --bs-table-striped-color: var(--text);
            --bs-table-hover-bg: var(--accent-soft);
            --bs-table-hover-color: var(--text);
            color: var(--text); margin-bottom: 0; border-color: var(--border);
        }
        .table > thead th, .table > thead td {
            background: var(--surface-2); color: var(--text-3);
            text-transform: uppercase; font-size: 11px; font-weight: 700;
            letter-spacing: 0.05em; border-bottom: 1px solid var(--border); white-space: nowrap;
        }
        .table > tbody td, .table > tbody th { border-color: var(--border); vertical-align: middle; }
        .table-light, .table-light > th, .table-light > td {
            --bs-table-bg: var(--surface-2); background: var(--surface-2); color: var(--text);
        }

        /* ============ Agent-type row accents ============ */
        /* Session rows are colour-coded by agent type with a slim rail on the
           row's leading edge that matches the agent's badge — and Core (the
           primary agent) keeps a faint accent wash so its rows still stand out.
           Replaces Bootstrap's off-palette .table-danger wash that tinted Core
           rows a muddy red. The rail is a pseudo-element, so it layers cleanly
           over the row-hover highlight and adds no layout shift. */
        tr.row-agent-core > td:first-child,
        tr.row-agent-high > td:first-child,
        tr.row-agent-low  > td:first-child,
        tr.row-agent-user > td:first-child { position: relative; }
        tr.row-agent-core > td:first-child::before,
        tr.row-agent-high > td:first-child::before,
        tr.row-agent-low  > td:first-child::before,
        tr.row-agent-user > td:first-child::before {
            content: ""; position: absolute; left: 0; top: 0; bottom: 0;
            width: 3px; border-radius: 0 2px 2px 0;
        }
        tr.row-agent-core > td:first-child::before { background: var(--accent); }
        tr.row-agent-high > td:first-child::before { background: var(--ok); }
        tr.row-agent-low  > td:first-child::before { background: var(--info); }
        tr.row-agent-user > td:first-child::before { background: var(--warn); }
        tr.row-agent-core { --bs-table-bg: var(--row-core-bg); }

        /* ============ Badges (soft pills) ============ */
        .badge {
            font-weight: 600; border-radius: 99px; padding: 0.34em 0.72em;
            font-size: 11px; border: 1px solid transparent; line-height: 1.5;
        }
        .badge.bg-primary   { background: var(--accent-soft) !important; color: var(--accent) !important; }
        .badge.bg-success   { background: var(--ok-soft) !important;     color: var(--ok) !important; }
        .badge.bg-info      { background: var(--info-soft) !important;   color: var(--info) !important; }
        .badge.bg-warning   { background: var(--warn-soft) !important;   color: var(--warn) !important; }
        .badge.bg-danger    { background: var(--danger-soft) !important; color: var(--danger) !important; }
        .badge.bg-secondary { background: var(--surface-2) !important;   color: var(--text-2) !important; border-color: var(--border); }
        /* Route legend pills carry an inline saturated background → white text. */
        .badge[style*="background"] { color: #fff !important; }

        /* ============ Buttons ============ */
        .btn { border-radius: 8px; font-weight: 500; }
        .btn-primary { background: var(--accent); border-color: var(--accent); color: #fff; }
        .btn-primary:hover { background: var(--accent); border-color: var(--accent); color: #fff; filter: brightness(1.08); }
        .btn-light {
            background: var(--surface-2); border-color: var(--border); color: var(--text-2);
        }
        .btn-light:hover { background: var(--surface-2); color: var(--accent); border-color: var(--accent); }
        .btn-outline-primary   { color: var(--accent); border-color: var(--accent); }
        .btn-outline-primary:hover   { background: var(--accent); border-color: var(--accent); color: #fff; }
        .btn-outline-secondary { color: var(--text-2); border-color: var(--border); }
        .btn-outline-secondary:hover { background: var(--surface-2); border-color: var(--accent); color: var(--accent); }
        .btn-outline-success { color: var(--ok); border-color: var(--ok); }
        .btn-outline-success:hover { background: var(--ok); border-color: var(--ok); color: #fff; }
        .btn-outline-info    { color: var(--info); border-color: var(--info); }
        .btn-outline-info:hover    { background: var(--info); border-color: var(--info); color: #fff; }
        .btn-outline-warning { color: var(--warn); border-color: var(--warn); }
        .btn-outline-warning:hover { background: var(--warn); border-color: var(--warn); color: #fff; }
        .btn-outline-danger  { color: var(--danger); border-color: var(--danger); }
        .btn-outline-danger:hover  { background: var(--danger); border-color: var(--danger); color: #fff; }

        /* ============ Alerts ============ */
        .alert { border-radius: var(--radius); border: 1px solid var(--border); }
        .alert-info    { background: var(--info-soft);   color: var(--info);   border-color: transparent; }
        .alert-warning { background: var(--warn-soft);   color: var(--warn);   border-color: transparent; }
        .alert-danger  { background: var(--danger-soft); color: var(--danger); border-color: transparent; }
        .alert-success { background: var(--ok-soft);     color: var(--ok);     border-color: transparent; }

        /* ============ Misc helpers (ported, recoloured) ============ */
        .text-justify { text-align: justify; }
        .config-table td { vertical-align: middle; }
        .config-value {
            font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
            background: var(--code-bg); border: 1px solid var(--border);
            padding: 0.2rem 0.5rem; border-radius: 5px; font-size: 0.86em;
        }
        .expandable-content { cursor: pointer; position: relative; }
        .expandable-content:hover {
            background: var(--surface-2); border-radius: 4px; padding: 2px 4px; margin: -2px -4px;
        }
        .expandable-content .expand-icon { color: var(--accent); font-weight: bold; margin-left: 4px; }
        .expandable-content.expanded .expand-icon::before { content: "▼"; }
        .expandable-content:not(.expanded) .expand-icon::before { content: "▶"; }
        .full-content {
            display: none; margin-top: 8px; padding: 8px;
            background: var(--surface-2); border-radius: 6px; border-left: 3px solid var(--accent);
        }
        .expandable-content.expanded .full-content { display: block; }

        /* ============ Collapsible cards / sections (native <details>) ============ */
        /* Collapsed by default; toggled by the browser, no JS. The <summary> reuses
           .card-header so a collapsible card matches a regular one. */
        .collapsible-card > summary.collapsible-summary {
            display: flex; align-items: center; gap: 10px;
            cursor: pointer; user-select: none; list-style: none;
        }
        .collapsible-card > summary::-webkit-details-marker { display: none; }
        .collapsible-summary .collapsible-title { flex: 1; font-weight: 600; }
        .collapsible-summary .collapsible-meta { display: flex; gap: 5px; align-items: center; flex-wrap: wrap; }
        .collapsible-card > summary::after {
            content: "\25B8"; color: var(--text-3); font-size: 13px; line-height: 1;
            transition: transform 0.15s ease; flex-shrink: 0;
        }
        .collapsible-card[open] > summary::after { transform: rotate(90deg); color: var(--accent); }
        .collapsible-card > summary:hover { background: var(--surface); }
        .collapsible-card > summary:hover::after { color: var(--accent); }
        /* When collapsed the header is the whole card: round all corners, drop the divider. */
        .collapsible-card:not([open]) > summary {
            border-bottom: none; border-radius: var(--radius) !important;
        }

        .collapsible-section {
            border: 1px solid var(--border); border-radius: 8px;
            margin-bottom: 8px; background: var(--surface-2); overflow: hidden;
        }
        .collapsible-section:last-child { margin-bottom: 0; }
        .collapsible-section > summary.collapsible-section-summary {
            display: flex; align-items: center; gap: 10px;
            cursor: pointer; user-select: none; list-style: none;
            padding: 8px 12px; font-size: 13px;
        }
        .collapsible-section > summary::-webkit-details-marker { display: none; }
        .collapsible-section > summary::before {
            content: "\25B8"; color: var(--text-3); font-size: 12px; line-height: 1;
            transition: transform 0.15s ease; flex-shrink: 0;
        }
        .collapsible-section[open] > summary::before { transform: rotate(90deg); color: var(--accent); }
        .collapsible-section > summary:hover { background: var(--surface); }
        .collapsible-section-title { flex: 1; font-weight: 600; }
        .collapsible-section-badges { display: flex; gap: 5px; align-items: center; flex-wrap: wrap; justify-content: flex-end; }
        .collapsible-section-body { padding: 0 12px 12px; }
        .collapsible-section-body pre {
            margin: 8px 0 0; max-height: 460px; overflow: auto;
            white-space: pre-wrap; word-break: break-word; font-size: 12px; line-height: 1.5;
        }
        .collapsible-section-body .alert { margin-top: 8px; margin-bottom: 0; }

        /* ============ Scrollbars ============ */
        ::-webkit-scrollbar { width: 10px; height: 10px; }
        ::-webkit-scrollbar-thumb { background: var(--border); border-radius: 99px; border: 2px solid transparent; background-clip: padding-box; }
        ::-webkit-scrollbar-track { background: transparent; }

        /* ============ Responsive ============ */
        @media (max-width: 860px) {
            .sidebar {
                position: fixed; z-index: 30; height: 100vh; left: 0; top: 0;
                transform: translateX(-100%); box-shadow: 0 0 40px rgba(0,0,0,0.25);
            }
            .app.sidebar-open .sidebar { transform: translateX(0); }
            .app.sidebar-open .sidebar-backdrop { display: block; }
            #sidebar-toggle { display: inline-flex; }
            .content-scroll { padding: 16px; }
        }
    `
}

// GetNavbarStyles is retained for backward compatibility. Navbar styling now
// lives entirely in GetStyles (the top navbar was replaced by a sidebar).
func GetNavbarStyles() string {
	return ""
}
