// HashForge - Common JavaScript

// Mobile menu toggle
function toggleMobileMenu() {
    const nav = document.getElementById('mainNav');
    const btn = document.querySelector('.mobile-menu-btn');
    let overlay = document.querySelector('.nav-overlay');

    // Create overlay if it doesn't exist
    if (!overlay) {
        overlay = document.createElement('div');
        overlay.className = 'nav-overlay';
        overlay.onclick = toggleMobileMenu;
        document.body.appendChild(overlay);
    }

    nav.classList.toggle('open');
    btn.classList.toggle('active');
    overlay.classList.toggle('active');

    // Prevent body scroll when menu is open
    document.body.style.overflow = nav.classList.contains('open') ? 'hidden' : '';
}

// Close mobile menu on window resize (if switching to desktop)
window.addEventListener('resize', function() {
    if (window.innerWidth > 768) {
        const nav = document.getElementById('mainNav');
        const btn = document.querySelector('.mobile-menu-btn');
        const overlay = document.querySelector('.nav-overlay');

        if (nav) nav.classList.remove('open');
        if (btn) btn.classList.remove('active');
        if (overlay) overlay.classList.remove('active');
        document.body.style.overflow = '';
    }
});

// Close mobile menu when clicking a nav link
document.addEventListener('DOMContentLoaded', function() {
    const nav = document.getElementById('mainNav');
    if (nav) {
        nav.querySelectorAll('a').forEach(link => {
            link.addEventListener('click', function() {
                if (window.innerWidth <= 768) {
                    toggleMobileMenu();
                }
            });
        });
    }
});

// Copy to clipboard
function copyToClipboard(elementId) {
    const el = document.getElementById(elementId);
    if (!el) return;

    el.select();
    el.setSelectionRange(0, 99999);

    navigator.clipboard.writeText(el.value).then(() => {
        // Show feedback
        const btn = el.nextElementSibling;
        if (btn) {
            const original = btn.textContent;
            btn.textContent = 'Copied!';
            btn.style.borderColor = 'var(--accent-green)';
            btn.style.color = 'var(--accent-green)';
            setTimeout(() => {
                btn.textContent = original;
                btn.style.borderColor = '';
                btn.style.color = '';
            }, 2000);
        }
    }).catch(() => {
        // Fallback for older browsers
        document.execCommand('copy');
    });
}

// CSRF token helper
function getCSRFToken() {
    const meta = document.querySelector('meta[name="csrf-token"]');
    return meta ? meta.getAttribute('content') : '';
}

// API helper with CSRF
async function apiRequest(method, url, data = null) {
    const options = {
        method: method,
        headers: {
            'Content-Type': 'application/json',
            'X-CSRF-Token': getCSRFToken()
        }
    };

    if (data) {
        options.body = JSON.stringify(data);
    }

    const response = await fetch(url, options);
    return response.json();
}

// Format numbers
function formatSats(sats) {
    return new Intl.NumberFormat().format(sats);
}

// Auto-refresh active order status
function startOrderRefresh(orderId, intervalMs = 30000) {
    if (!orderId) return;

    setInterval(async () => {
        try {
            const data = await apiRequest('GET', `/api/v1/rental/order/${orderId}`);
            if (data.order) {
                // Update UI elements if they exist
                const spentEl = document.getElementById('order-spent');
                const progressEl = document.getElementById('order-progress');

                if (spentEl) spentEl.textContent = formatSats(data.order.amount_spent_sat);
                if (progressEl) progressEl.textContent = data.progress_pct.toFixed(1) + '%';

                // Reload if order completed
                if (data.order.status !== 'active' && data.order.status !== 'pending') {
                    location.reload();
                }
            }
        } catch (e) {
            console.error('Failed to refresh order:', e);
        }
    }, intervalMs);
}

// Confirm dangerous actions
document.addEventListener('DOMContentLoaded', function() {
    // Add confirmation to forms with data-confirm attribute
    document.querySelectorAll('form[data-confirm]').forEach(form => {
        form.addEventListener('submit', function(e) {
            if (!confirm(this.dataset.confirm)) {
                e.preventDefault();
            }
        });
    });
});

// Register Service Worker for PWA
if ('serviceWorker' in navigator) {
    window.addEventListener('load', function() {
        navigator.serviceWorker.register('/static/sw.js')
            .then(function(registration) {
                console.log('ServiceWorker registered:', registration.scope);
            })
            .catch(function(error) {
                console.log('ServiceWorker registration failed:', error);
            });
    });
}
