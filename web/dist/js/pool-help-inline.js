        // Smooth scroll for anchor links
        document.querySelectorAll('.help-nav a').forEach(link => {
            link.addEventListener('click', function(e) {
                const href = this.getAttribute('href');
                if (href.startsWith('#')) {
                    e.preventDefault();
                    const target = document.querySelector(href);
                    if (target) {
                        target.scrollIntoView({ behavior: 'smooth', block: 'start' });
                        // Update URL without scrolling
                        history.pushState(null, null, href);
                    }
                }
            });
        });

        // Highlight current section in nav on scroll
        const sections = document.querySelectorAll('.help-section');
        const navLinks = document.querySelectorAll('.help-nav a');

        function updateActiveNav() {
            let current = '';
            sections.forEach(section => {
                const sectionTop = section.offsetTop - 100;
                if (window.scrollY >= sectionTop) {
                    current = section.getAttribute('id');
                }
            });
            navLinks.forEach(link => {
                link.style.borderColor = link.getAttribute('href') === '#' + current ? 'var(--bch-green)' : '';
                link.style.color = link.getAttribute('href') === '#' + current ? 'var(--bch-green)' : '';
            });
        }

        window.addEventListener('scroll', updateActiveNav);
        updateActiveNav();
