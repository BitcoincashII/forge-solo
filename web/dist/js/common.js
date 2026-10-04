/**
 * Forge Solo - Common JavaScript Utilities
 * Shared functions used across all pages
 */

// XSS protection: sanitize all dynamic content before inserting into HTML
function sanitizeHTML(str) {
    if (str === null || str === undefined) return '';
    return String(str)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#x27;');
}

// Format hashrate with appropriate unit
function formatHashrate(h) {
    if (!h || h === 0) return '0 H/s';
    const units = ['H/s', 'KH/s', 'MH/s', 'GH/s', 'TH/s', 'PH/s', 'EH/s'];
    let i = 0;
    while (h >= 1000 && i < units.length - 1) {
        h /= 1000;
        i++;
    }
    return h.toFixed(2) + ' ' + units[i];
}

// Format relative time (e.g., "5m ago", "2h ago")
function timeAgo(timestamp) {
    if (!timestamp) return 'Never';
    const now = Date.now();
    const time = typeof timestamp === 'number' ? timestamp * 1000 : new Date(timestamp).getTime();
    const diff = Math.floor((now - time) / 1000);

    if (diff < 0) return 'Just now';
    if (diff < 60) return diff + 's ago';
    if (diff < 3600) return Math.floor(diff / 60) + 'm ago';
    if (diff < 86400) return Math.floor(diff / 3600) + 'h ago';
    if (diff < 604800) return Math.floor(diff / 86400) + 'd ago';
    return new Date(time).toLocaleDateString();
}

// Format difficulty with appropriate suffix
function formatDiff(d) {
    if (!d || d === 0) return '0';
    if (d >= 1e15) return (d / 1e15).toFixed(2) + 'P';
    if (d >= 1e12) return (d / 1e12).toFixed(2) + 'T';
    if (d >= 1e9) return (d / 1e9).toFixed(2) + 'G';
    if (d >= 1e6) return (d / 1e6).toFixed(2) + 'M';
    if (d >= 1e3) return (d / 1e3).toFixed(2) + 'K';
    return d.toFixed(2);
}

// Format BCH2 amount
function formatBCH2(amount, decimals = 4) {
    if (!amount && amount !== 0) return '0';
    return Number(amount).toFixed(decimals);
}

// Format large numbers with commas
function formatNumber(num) {
    if (!num && num !== 0) return '0';
    return Number(num).toLocaleString();
}

// Copy text to clipboard with visual feedback
async function copyText(text, buttonElement) {
    try {
        await navigator.clipboard.writeText(text);

        // Visual feedback
        if (buttonElement) {
            const originalText = buttonElement.textContent;
            const originalClass = buttonElement.className;
            buttonElement.textContent = 'Copied!';
            buttonElement.classList.add('copy-success');

            setTimeout(() => {
                buttonElement.textContent = originalText;
                buttonElement.className = originalClass;
            }, 2000);
        }
        return true;
    } catch (err) {
        console.error('Failed to copy:', err);
        // Fallback for older browsers
        const textarea = document.createElement('textarea');
        textarea.value = text;
        textarea.style.position = 'fixed';
        textarea.style.opacity = '0';
        document.body.appendChild(textarea);
        textarea.select();
        try {
            document.execCommand('copy');
            if (buttonElement) {
                const originalText = buttonElement.textContent;
                buttonElement.textContent = 'Copied!';
                setTimeout(() => {
                    buttonElement.textContent = originalText;
                }, 2000);
            }
            return true;
        } catch (e) {
            console.error('Fallback copy failed:', e);
            return false;
        } finally {
            document.body.removeChild(textarea);
        }
    }
}

// Validate BCH2 address format (basic validation)
function isValidBCH2Address(address) {
    if (!address || typeof address !== 'string') return false;
    // BCH2 addresses are typically base58 or bech32 format
    // This is a basic validation - adjust based on actual BCH2 address format
    return /^[a-zA-Z0-9]{25,64}$/.test(address.trim());
}

// Validate block hash format (64 hex characters)
function isValidBlockHash(hash) {
    if (!hash || typeof hash !== 'string') return false;
    return /^[a-fA-F0-9]{64}$/.test(hash);
}

// Truncate hash for display
function truncateHash(hash, startChars = 12, endChars = 8) {
    if (!hash || hash.length <= startChars + endChars) return hash || '';
    return hash.substring(0, startChars) + '...' + hash.substring(hash.length - endChars);
}

// Show loading state for an element
function showLoading(elementId, message = 'Loading...') {
    const el = document.getElementById(elementId);
    if (el) {
        el.innerHTML = `<div class="loading-state"><div class="loading-spinner"></div><span>${sanitizeHTML(message)}</span></div>`;
    }
}

// Show error state for an element
function showError(elementId, message = 'Failed to load data') {
    const el = document.getElementById(elementId);
    if (el) {
        el.innerHTML = `<div class="error-state"><span class="error-icon">!</span><span>${sanitizeHTML(message)}</span></div>`;
    }
}

// Show empty state for an element
function showEmpty(elementId, message = 'No data available') {
    const el = document.getElementById(elementId);
    if (el) {
        el.innerHTML = `<div class="empty-state">${sanitizeHTML(message)}</div>`;
    }
}

// Connection status management
const ConnectionStatus = {
    isOnline: navigator.onLine,
    listeners: [],

    init() {
        window.addEventListener('online', () => this.setStatus(true));
        window.addEventListener('offline', () => this.setStatus(false));
        this.updateUI();
    },

    setStatus(online) {
        this.isOnline = online;
        this.updateUI();
        this.listeners.forEach(fn => fn(online));
    },

    updateUI() {
        const banner = document.getElementById('offlineBanner');
        if (banner) {
            banner.style.display = this.isOnline ? 'none' : 'flex';
        }
    },

    onStatusChange(callback) {
        this.listeners.push(callback);
    }
};

// API fetch wrapper with error handling
async function apiFetch(url, options = {}) {
    const defaultOptions = {
        headers: {
            'Content-Type': 'application/json',
        },
    };

    const mergedOptions = { ...defaultOptions, ...options };

    try {
        const response = await fetch(url, mergedOptions);

        if (!response.ok) {
            // The API's own reason, when it gave one ("the database is not answering"), goes with
            // the error for the page to show.
            let body = null;
            try { body = await response.json(); } catch (e) { body = null; }
            const err = new Error(`HTTP ${response.status}: ${response.statusText}`);
            err.status = response.status;
            err.apiError = (body && typeof body.error === 'string') ? body.error : '';
            throw err;
        }

        return await response.json();
    } catch (error) {
        console.error(`API fetch error (${url}):`, error);
        throw error;
    }
}

// Debounce function for search/input handlers
function debounce(func, wait) {
    let timeout;
    return function executedFunction(...args) {
        const later = () => {
            clearTimeout(timeout);
            func(...args);
        };
        clearTimeout(timeout);
        timeout = setTimeout(later, wait);
    };
}

// Local storage helpers with error handling
const Storage = {
    get(key, defaultValue = null) {
        try {
            const item = localStorage.getItem(key);
            return item ? JSON.parse(item) : defaultValue;
        } catch (e) {
            console.error('Storage get error:', e);
            return defaultValue;
        }
    },

    set(key, value) {
        try {
            localStorage.setItem(key, JSON.stringify(value));
            return true;
        } catch (e) {
            console.error('Storage set error:', e);
            return false;
        }
    },

    remove(key) {
        try {
            localStorage.removeItem(key);
            return true;
        } catch (e) {
            console.error('Storage remove error:', e);
            return false;
        }
    }
};

// Modal management
const Modal = {
    show(modalId) {
        const modal = document.getElementById(modalId);
        if (modal) {
            modal.classList.add('active');
            modal.setAttribute('aria-hidden', 'false');
            document.body.style.overflow = 'hidden';

            // Focus first focusable element
            const focusable = modal.querySelector('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])');
            if (focusable) focusable.focus();
        }
    },

    hide(modalId) {
        const modal = document.getElementById(modalId);
        if (modal) {
            modal.classList.remove('active');
            modal.setAttribute('aria-hidden', 'true');
            document.body.style.overflow = '';
        }
    },

    init() {
        // Close modal on backdrop click
        document.querySelectorAll('.modal-overlay').forEach(modal => {
            modal.addEventListener('click', (e) => {
                if (e.target === modal) {
                    this.hide(modal.id);
                }
            });
        });

        // Close modal on Escape key
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape') {
                const activeModal = document.querySelector('.modal-overlay.active');
                if (activeModal) {
                    this.hide(activeModal.id);
                }
            }
        });
    }
};

// Forge Solo 1.0.13 keeps its data in a new database, and moves an earlier version's data into it
// once. The API's health answer says what came of that. Status 'maintenance': the move was needed
// and failed, so Forge Solo does not mine and the API answers nothing else; every page shows one
// notice in its place, with what to do. migration.state: the API runs as usual, and a banner says
// why the old data is not in the new database, or not yet.
const OldData = {
    banner: {
        deferred: 'The data of your earlier Forge Solo is not in the new database yet: the database was in use when the move ran. The move finishes at the next restart of Forge Solo.',
        degraded: 'The database folder from before 1.0.13 is damaged or partly deleted and is ignored. What was moved from it at the update is in the new database.',
        skipped: 'Forge Solo started without the data of its earlier version, as chosen. That data is kept as it was: Settings can bring it in.',
        failed: 'Moving the data of the earlier version into the new database failed. Restart Forge Solo to try again.'
    },
    // Where the Settings password is, as Settings says it.
    pwWhere: {
        umbrel: 'It is the Default password umbrelOS shows for Forge Solo: right-click the Forge Solo icon on the umbrelOS home screen, then Settings → Default credentials. It is not your Umbrel login password.',
        windows: 'Right-click the Forge Solo icon in the notification area of the taskbar and choose Copy Settings Password, then paste it here.',
        linux: 'It is DASHBOARD_PASSWORD in secrets.env.'
    },
    PW_KEY: 'forgeSoloPassword',

    init() {
        this.read().then(h => {
            if (!h) return;
            if (h.status === 'maintenance') {
                this.showNotice(h);
            } else if (h.migration && this.banner[h.migration.state]) {
                this.showBanner(h.migration);
            }
        });
    },

    // The API's health answer, or null when there is none.
    read() {
        return fetch('/api/v1/health', { cache: 'no-store' })
            .then(r => r.json().catch(() => null))
            .then(d => (d && typeof d === 'object') ? d : null)
            .catch(() => null);
    },

    showBanner(m) {
        const el = document.createElement('div');
        el.className = 'migration-banner';
        el.id = 'migrationBanner';
        el.setAttribute('role', 'status');
        el.textContent = this.banner[m.state];
        // At the top of the page's content, as wide as its cards.
        (document.querySelector('main .container') || document.querySelector('main') || document.body).prepend(el);
    },

    // The notice over the whole page while the move has failed. The figures behind it cannot be
    // read, and Forge Solo is not mining.
    showNotice(h) {
        this.where = this.pwWhere[h.platform] || this.pwWhere.umbrel;
        const code = h.code ? ' (code ' + sanitizeHTML(h.code) + ')' : '';
        const el = document.createElement('div');
        el.className = 'maintenance-notice';
        el.id = 'maintenanceNotice';
        el.setAttribute('role', 'alert');
        el.innerHTML = `<div class="maintenance-card">
            <h1>Forge Solo could not move its data</h1>
            <p>Forge Solo now keeps its data in a new database. Moving the data of the earlier version into it failed: <strong>${sanitizeHTML(h.reason || 'see the log')}</strong>${code}.</p>
            <p><strong>Nothing was lost.</strong> The old data is as it was, and nothing was replaced. Forge Solo does not mine until this is settled; the nodes keep running.</p>
            <p><strong>Restart Forge Solo to try again.</strong></p>
            <div class="maintenance-skip">
                <p>If the move fails again, Forge Solo can start without the old data, with a new, empty database. The old data stays where it is, and Settings can bring it in later. Your payout address is part of it: set it again in Settings after the restart.</p>
                <label for="maintenancePw">Forge Solo password</label>
                <input id="maintenancePw" type="text" autocomplete="off" data-lpignore="true" data-1p-ignore="true" data-bwignore="true" data-form-type="other" spellcheck="false" autocapitalize="none" autocorrect="off">
                <p class="maintenance-note">${sanitizeHTML(this.where)}</p>
                <button type="button" id="maintenanceSkipBtn"></button>
                <p class="maintenance-status" id="maintenanceStatus" role="status"></p>
            </div>
        </div>`;
        document.body.appendChild(el);
        document.body.classList.add('in-maintenance');
        try { document.getElementById('maintenancePw').value = localStorage.getItem(this.PW_KEY) || ''; } catch (e) { /* no storage */ }
        this.setChosen(h.skip_file === true, '');
        document.getElementById('maintenanceSkipBtn').addEventListener('click', () => this.choose(!this.chosen));
        // Once Forge Solo has been restarted and runs again, the page is shown as usual.
        setInterval(() => this.read().then(d => { if (d && d.status !== 'maintenance') location.reload(); }), 10000);
    },

    // chosen: SKIP-POSTGRES-MIGRATION is there, so the next start goes without the old data.
    setChosen(chosen, message) {
        this.chosen = chosen;
        document.getElementById('maintenanceSkipBtn').textContent = chosen ? 'Try the move again instead' : 'Start without the old data';
        document.getElementById('maintenanceStatus').textContent = message ||
            (chosen ? 'You chose to start without the old data. Restart Forge Solo: it then starts with a new, empty database.' : '');
    },

    // Records the choice with Forge Solo, behind its Settings password as every change.
    choose(skip) {
        const st = document.getElementById('maintenanceStatus');
        const box = document.getElementById('maintenancePw');
        const pw = box.value.trim();
        const headers = { 'Content-Type': 'application/json' };
        if (pw) headers['X-Forge-Password'] = pw;
        st.textContent = 'Saving…';
        fetch('/api/v1/old-data', { method: 'POST', headers: headers, body: JSON.stringify({ skip: skip }) })
            .then(r => r.json().catch(() => null), () => null)
            .then(d => {
                if (d && d.success) {
                    try { if (pw) localStorage.setItem(this.PW_KEY, pw); } catch (e) { /* no storage */ }
                    this.setChosen(skip, d.message);
                } else if (d && d.password_required) {
                    if (d.password_wrong) {
                        try { localStorage.removeItem(this.PW_KEY); } catch (e) { /* no storage */ }
                        box.value = '';
                    }
                    st.textContent = (d.password_wrong ? 'That is not Forge Solo\'s password.' : 'Enter Forge Solo\'s password first.') + ' Nothing was changed. ' + this.where;
                    box.focus();
                } else if (d && d.error) {
                    st.textContent = d.error;
                } else {
                    st.textContent = 'Forge Solo is not answering right now, so nothing was changed. Try again in a minute.';
                }
            });
    }
};

// Initialize common functionality when DOM is ready
document.addEventListener('DOMContentLoaded', () => {
    ConnectionStatus.init();
    Modal.init();
    OldData.init();
});

// Export for module usage (if needed)
if (typeof module !== 'undefined' && module.exports) {
    module.exports = {
        sanitizeHTML,
        formatHashrate,
        timeAgo,
        formatDiff,
        formatBCH2,
        formatNumber,
        copyText,
        isValidBCH2Address,
        isValidBlockHash,
        truncateHash,
        showLoading,
        showError,
        showEmpty,
        ConnectionStatus,
        apiFetch,
        debounce,
        Storage,
        Modal,
        OldData
    };
}
