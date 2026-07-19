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
            throw new Error(`HTTP ${response.status}: ${response.statusText}`);
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

// Initialize common functionality when DOM is ready
document.addEventListener('DOMContentLoaded', () => {
    ConnectionStatus.init();
    Modal.init();
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
        Modal
    };
}
