// Display units shared by every page: imperial (ft, mph, °F) or metric
// (m, km/h, °C). The choice lives in localStorage, so the map, the station
// pages it embeds in an iframe, and other tabs all follow one setting.
//
// Data stays in its source units. Server-rendered readings are marked up
// with their imperial value, and apply() rewrites them for the viewer:
//
//   <span data-u="h" data-v="3.6">3.6ft</span>        height in feet
//   <span data-u="h" data-v="3.6" data-sp>3.6 ft</span> with a space
//   <span data-u="s" data-v="12">12 mph</span>        speed in mph
//   <span data-u="sn" data-v="18">18</span>           bare speed number (gusts)
//   <span data-u="t" data-v="15">15°C</span>          temperature in °C
//   <span data-u="surf">3–4ft</span>                  surf range label
//   <span data-u="hu"></span> / data-u="HU"            "ft"/"m" (upper case)
//
// [data-units-set="imperial|metric"] buttons choose the system; they sit in
// the options menu (see the menu wiring at the end). Charts read the
// helpers below while drawing and listen for "units:change" to redraw.
(function () {
    const KEY = 'openswells.units';
    const FT_PER_M = 3.28084;
    const KMH_PER_MPH = 1.609344;

    function stored() {
        try {
            const v = localStorage.getItem(KEY);
            if (v === 'metric' || v === 'imperial') return v;
        } catch (_) { /* storage blocked: fall back to the default */ }
        return null;
    }

    let system = stored() || 'imperial';

    const fixed = (n, digits) => {
        const s = Number(n).toFixed(digits);
        return s === '-0' || /^-0\.0*$/.test(s) ? s.slice(1) : s;
    };

    const Units = {
        get system() { return system; },
        get metric() { return system === 'metric'; },
        // unit labels
        get h() { return system === 'metric' ? 'm' : 'ft'; },
        get s() { return system === 'metric' ? 'km/h' : 'mph'; },
        get t() { return system === 'metric' ? '°C' : '°F'; },

        // Numeric conversions into the display unit.
        ftVal(ft) { return system === 'metric' ? ft / FT_PER_M : ft; },
        mVal(m) { return system === 'metric' ? m : m * FT_PER_M; },
        mphVal(mph) { return system === 'metric' ? mph * KMH_PER_MPH : mph; },
        msVal(ms) { return Units.mphVal(ms * 2.23694); },

        // Formatted readings. `sp` puts a space before the unit ("3.6 ft").
        height(ft, { digits = 1, sp = true } = {}) {
            if (ft === null || ft === undefined || !Number.isFinite(Number(ft))) return '--';
            return `${fixed(Units.ftVal(Number(ft)), digits)}${sp ? ' ' : ''}${Units.h}`;
        },
        heightM(m, opts) { return Units.height(Number(m) * FT_PER_M, opts); },
        speed(mph, { sp = true } = {}) {
            if (mph === null || mph === undefined || !Number.isFinite(Number(mph))) return '--';
            return `${Math.round(Units.mphVal(Number(mph)))}${sp ? ' ' : ''}${Units.s}`;
        },
        speedMs(ms, opts) { return Units.speed(Number(ms) * 2.23694, opts); },
        temp(c) {
            if (c === null || c === undefined || !Number.isFinite(Number(c))) return '--';
            const v = system === 'metric' ? Number(c) : Number(c) * 9 / 5 + 32;
            return `${Math.round(v)}${Units.t}`;
        },

        // Surf range labels come from the server in whole feet ("3–4ft",
        // "<1ft"); metric shows the same range in tenths of a meter.
        surf(label) {
            if (!label || system !== 'metric') return label;
            return String(label).replace(/(<?)(\d+(?:\.\d+)?)(?:\s*[–-]\s*(\d+(?:\.\d+)?))?\s*ft\b/g, (_, lt, a, b) => {
                const m = v => fixed(Number(v) / FT_PER_M, 1);
                return `${lt}${m(a)}${b !== undefined ? `–${m(b)}` : ''}m`;
            });
        },

        set(next) {
            if (next !== 'metric' && next !== 'imperial') return;
            const changed = next !== system;
            system = next;
            try { localStorage.setItem(KEY, next); } catch (_) { /* session-only */ }
            if (changed) announce();
        },
        toggle() { Units.set(system === 'metric' ? 'imperial' : 'metric'); },

        // Rewrite every marked reading under root for the current system.
        apply(root = document) {
            if (!root.querySelectorAll) return;
            const nodes = [...root.querySelectorAll('[data-u]')];
            if (root.matches && root.matches('[data-u]')) nodes.push(root);
            for (const el of nodes) {
                const kind = el.dataset.u;
                if (kind === 'surf') {
                    if (el.dataset.orig === undefined) el.dataset.orig = el.textContent.trim();
                    el.textContent = Units.surf(el.dataset.orig);
                    continue;
                }
                if (kind === 'hu' || kind === 'HU') {
                    el.textContent = kind === 'HU' ? Units.h.toUpperCase() : Units.h;
                    continue;
                }
                if (kind === 'su' || kind === 'SU') {
                    el.textContent = kind === 'SU' ? Units.s.toUpperCase() : Units.s;
                    continue;
                }
                const v = el.dataset.v;
                if (v === undefined || v === '' || !Number.isFinite(Number(v))) continue;
                const sp = el.hasAttribute('data-sp');
                if (kind === 'h') el.textContent = Units.height(v, { sp });
                else if (kind === 's') el.textContent = Units.speed(v, { sp });
                else if (kind === 'sn') el.textContent = String(Math.round(Units.mphVal(Number(v))));
                else if (kind === 't') el.textContent = Units.temp(v);
            }
            syncToggles(root);
        },
    };

    function syncToggles(root = document) {
        if (!root.querySelectorAll) return;
        root.querySelectorAll('[data-units-set]').forEach(btn => {
            btn.setAttribute('aria-checked', String(btn.dataset.unitsSet === system));
        });
    }

    function announce() {
        Units.apply(document);
        window.dispatchEvent(new CustomEvent('units:change', { detail: { system } }));
    }

    // Another tab, or the map hosting this page in an iframe, changed it.
    window.addEventListener('storage', e => {
        if (e.key !== KEY) return;
        const next = stored() || 'imperial';
        if (next !== system) { system = next; announce(); }
    });

    // Options menus. A [data-menu-root] holds one [data-menu-trigger] and
    // one [data-menu]; clicks inside an open menu leave it open so several
    // settings can be changed in one visit.
    function closeMenus(except) {
        document.querySelectorAll('[data-menu]:not([hidden])').forEach(menu => {
            if (menu === except) return;
            menu.hidden = true;
            menu.closest('[data-menu-root]')?.querySelector('[data-menu-trigger]')?.setAttribute('aria-expanded', 'false');
        });
    }

    function openMenu(trigger, menu) {
        menu.classList.remove('menu-left');
        menu.hidden = false;
        trigger.setAttribute('aria-expanded', 'true');
        // anchored to the trigger's right edge unless that runs off screen
        if (menu.getBoundingClientRect().left < 8) menu.classList.add('menu-left');
    }

    document.addEventListener('click', e => {
        const target = e.target.closest ? e.target : null;
        const set = target && target.closest('[data-units-set]');
        if (set) { Units.set(set.dataset.unitsSet); return; }
        const trigger = target && target.closest('[data-menu-trigger]');
        if (trigger) {
            const menu = trigger.closest('[data-menu-root]')?.querySelector('[data-menu]');
            if (!menu) return;
            const wasOpen = !menu.hidden;
            closeMenus();
            if (!wasOpen) openMenu(trigger, menu);
            return;
        }
        closeMenus(target && target.closest('[data-menu]'));
    });

    document.addEventListener('keydown', e => {
        if (e.key !== 'Escape') return;
        const open = document.querySelector('[data-menu]:not([hidden])');
        if (!open) return;
        const trigger = open.closest('[data-menu-root]')?.querySelector('[data-menu-trigger]');
        closeMenus();
        if (trigger) trigger.focus();
    });

    // Fragments added later (htmx swaps, favorites cards) convert themselves.
    function watch() {
        Units.apply(document);
        new MutationObserver(records => {
            for (const r of records) {
                for (const n of r.addedNodes) if (n.nodeType === 1) Units.apply(n);
            }
        }).observe(document.body, { childList: true, subtree: true });
    }
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', watch);
    else watch();

    // Pages whose charts are too entangled to redraw in place reload on a
    // change instead; they call this once.
    Units.reloadOnChange = () => window.addEventListener('units:change', () => location.reload());

    window.Units = Units;
})();
