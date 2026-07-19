        let minerAddress = new URLSearchParams(window.location.search).get('address') || decodeURIComponent(window.location.pathname.split('/solo/')[1] || '');
        document.getElementById('minerAddress').textContent = minerAddress;

        document.getElementById('copyAddressBtn').addEventListener('click', function() {
            copyText(minerAddress, this);
        });

        let hashrateChart;
        let hashrateHistory = [];
        let networkDiff = 1;
        let minerBlocksCount = 0;
        let currentHashrateTH = 0;   // latest 5m hashrate (TH/s), for the stable avg-effort estimate

        // Stable per-miner average effort: your ACTUAL block cadence vs the cadence
        // EXPECTED at your hashrate. Unlike the single-round bar this barely moves, so it
        // shows whether your effort is really normal (~100%) rather than a jumpy snapshot.
        function updateAvgEffort(blocks) {
            const el = document.getElementById('avgEffort');
            if (!el) return;
            // Recent BCH2 solo blocks only (1175 blocks use a different network difficulty).
            const times = (blocks || [])
                .filter(b => b && b.coin !== '1175' && b.time)
                .map(b => Number(b.time))
                .filter(t => t > 0)
                .sort((a, b) => b - a)
                .slice(0, 12);
            if (times.length < 3 || currentHashrateTH <= 0 || networkDiff <= 0) { el.textContent = '--'; return; }
            const spanSec = times[0] - times[times.length - 1];
            const gaps = times.length - 1;
            const expectedGapSec = (networkDiff * 4294967296) / (currentHashrateTH * 1e12);
            if (spanSec <= 0 || gaps <= 0 || expectedGapSec <= 0) { el.textContent = '--'; return; }
            const avgEffort = (spanSec / gaps) / expectedGapSec * 100;
            el.textContent = avgEffort.toFixed(0) + '%';
            el.style.color = avgEffort <= 130 ? 'var(--bch-green)' : (avgEffort <= 180 ? 'var(--gold)' : 'var(--red)');
        }

        async function fetchStats() {
            try {
                const data = await apiFetch('/api/v1/stats');
                networkDiff = data.networkDifficulty || 1;
                document.getElementById('networkDiff').textContent = formatDiff(networkDiff);
                document.getElementById('networkHashrate').textContent = data.networkHashrate ? formatHashrate(data.networkHashrate) : '--';
            } catch(e) {
                console.error('Failed to fetch stats', e);
            }
        }

        async function fetchMinerData() {
            try {
                const data = await apiFetch('/api/v1/miners/' + encodeURIComponent(minerAddress));
                // Solo-only home app: always render this dashboard.
                document.getElementById('matureBalance').textContent = formatBCH2(data.matureBalance || 0, 2);
                document.getElementById('immatureBalance').textContent = formatBCH2(data.immatureBalance || 0, 2);
                document.getElementById('hashrate5m').textContent = formatHashrate((data.hashrate5m || 0) * 1e12);
                document.getElementById('hashrate60m').textContent = formatHashrate((data.hashrate60m || 0) * 1e12);
                document.getElementById('workers').textContent = formatNumber(data.workers || 0);
                document.getElementById('validShares').textContent = formatNumber(data.validShares || 0);
                document.getElementById('roundShares').textContent = formatNumber(data.validShares || 0);
                document.getElementById('bestDiff').textContent = formatDiff(data.bestDiff || 0);
                const rejectRate = data.invalidShares > 0 ?
                    ((data.invalidShares / (data.validShares + data.invalidShares)) * 100).toFixed(2) : '0.00';
                document.getElementById('rejectRate').textContent = rejectRate + '%';
                const workDone = data.totalWork || 0;
                const effort = networkDiff > 0 ? (workDone / networkDiff * 100) : 0;
                document.getElementById('currentEffort').textContent = effort.toFixed(1) + '%';
                const barWidth = Math.min(effort / 2, 100);
                const effortBar = document.getElementById('effortBar');
                const effortBarContainer = document.getElementById('effortBarContainer');
                effortBar.style.width = barWidth + '%';
                effortBarContainer.setAttribute('aria-valuenow', Math.round(effort));
                if (effort < 50) {
                    effortBar.style.background = 'linear-gradient(90deg, var(--bch-green), var(--gold))';
                } else if (effort < 100) {
                    effortBar.style.background = 'linear-gradient(90deg, var(--gold), var(--gold-dark))';
                } else {
                    effortBar.style.background = 'linear-gradient(90deg, var(--gold-dark), var(--red))';
                }
                const hashrate = data.hashrate5m || 0;
                currentHashrateTH = hashrate;
                if (hashrate > 0 && networkDiff > 0) {
                    const hashesPerSecond = hashrate * 1e12;
                    const hashesNeeded = networkDiff * 4294967296;
                    const secondsToBlock = hashesNeeded / hashesPerSecond;
                    if (secondsToBlock < 60) {
                        document.getElementById('timeToBlock').textContent = Math.round(secondsToBlock) + ' sec';
                    } else if (secondsToBlock < 3600) {
                        document.getElementById('timeToBlock').textContent = Math.round(secondsToBlock / 60) + ' min';
                    } else if (secondsToBlock < 86400) {
                        document.getElementById('timeToBlock').textContent = (secondsToBlock / 3600).toFixed(1) + ' hours';
                    } else {
                        document.getElementById('timeToBlock').textContent = (secondsToBlock / 86400).toFixed(1) + ' days';
                    }
                } else {
                    document.getElementById('timeToBlock').textContent = '--';
                }
                hashrateHistory.push(data.hashrate5m || 0);
                if (hashrateHistory.length > 288) hashrateHistory.shift();
                updateChart();
            } catch(e) {
                console.error('Failed to fetch miner data', e);
            }
        }

        async function fetchWorkers() {
            const tbody = document.getElementById('workersTable');
            try {
                const data = await apiFetch('/api/v1/miners/' + encodeURIComponent(minerAddress) + '/workers');
                if (!data.workers || data.workers.length === 0) {
                    tbody.innerHTML = '<tr><td colspan="6"><div class="empty-state" data-i18n="p_solo_no_workers">' + (typeof PT !== 'undefined' && PT.p_solo_no_workers ? PT.p_solo_no_workers : 'No workers connected') + '</div></td></tr>';
                    return;
                }
                tbody.innerHTML = data.workers.map(w => `
                    <tr>
                        <td><span class="status-dot ${w.online ? 'online' : 'offline'}" aria-hidden="true"></span>${sanitizeHTML(w.name || 'default')}</td>
                        <td style="color:var(--gold);font-weight:600">${formatNumber(w.blocksFound || 0)}</td>
                        <td>${formatHashrate((w.hashrate5m || 0) * 1e12)}</td>
                        <td>${formatHashrate((w.hashrate60m || 0) * 1e12)}</td>
                        <td style="color:var(--gold)">${formatDiff(w.roundBestDiff || w.bestDiff || 0)}</td>
                        <td style="color:var(--bch-green)">${formatDiff(w.athDiff || w.bestDiff || 0)}</td>
                    </tr>
                `).join('');
            } catch(e) {
                console.error('Failed to fetch workers', e);
                tbody.innerHTML = '<tr><td colspan="6"><div class="error-state"><span class="error-icon">!</span><span data-i18n="p_error_load_workers">' + (typeof PT !== 'undefined' && PT.p_error_load_workers ? PT.p_error_load_workers : 'Failed to load workers') + '</span></div></td></tr>';
            }
        }

        async function fetchBlocks() {
            const tbody = document.getElementById('blocksTable');
            try {
                const data = await apiFetch('/api/v1/miners/' + encodeURIComponent(minerAddress) + '/solo-blocks');
                if (!data.blocks || data.blocks.length === 0) {
                    tbody.innerHTML = '<tr><td colspan="7"><div class="empty-state" data-i18n="p_solo_no_blocks">' + (typeof PT !== 'undefined' && PT.p_solo_no_blocks ? PT.p_solo_no_blocks : 'No blocks found yet. Keep mining!') + '</div></td></tr>';
                    minerBlocksCount = 0;
                    document.getElementById('blocksFound').textContent = '0';
                    document.getElementById('totalEarned').textContent = 'Total: 0 BCH2';
                } else {
                    const sorted = data.blocks.slice().sort((a, b) => (b.time || 0) - (a.time || 0));
                    minerBlocksCount = sorted.length;
                    var confirmedText = typeof PT !== 'undefined' && PT.p_status_confirmed ? PT.p_status_confirmed : 'Confirmed';
                    var pendingText = typeof PT !== 'undefined' && PT.p_status_pending ? PT.p_status_pending : 'Pending';
                    var processingText = typeof PT !== 'undefined' && PT.p_status_processing ? PT.p_status_processing : 'Processing';
                    let bch2Reward = 0, esfReward = 0, esfCount = 0;
                    tbody.innerHTML = sorted.slice(0, 20).map(b => {
                        const is1175 = b.coin === '1175';
                        const coinBadge = is1175
                            ? '<span style="background:rgba(224,179,65,0.15);color:#e0b341;padding:2px 7px;border-radius:4px;font-size:11px;font-weight:700">1175</span>'
                            : '<span style="background:rgba(10,193,142,0.15);color:#0ac18e;padding:2px 7px;border-radius:4px;font-size:11px;font-weight:700">BCH2</span>';
                        const safeHash = isValidBlockHash(b.hash) ? b.hash : '';
                        const safeTxid = b.payoutTxid && isValidBlockHash(b.payoutTxid) ? b.payoutTxid : '';
                        // Block cell: BCH2 links to the BCH2 explorer; 1175 shows the hash without a wrong-chain link.
                        let blockCell;
                        if (!safeHash) blockCell = 'N/A';
                        else if (is1175) blockCell = `<span style="color:var(--text-secondary)" title="1175 block">${truncateHash(safeHash, 8, 4)}</span>`;
                        else blockCell = `<a href="https://explorer.bch2.org/block/${safeHash}" target="_blank" rel="noopener noreferrer" class="hash-link" title="View this block on the BCH2 explorer">${truncateHash(safeHash, 8, 4)}</a>`;
                        // Payout cell: BCH2 links to the tx; 1175 shows status text (BCH2 explorer would be wrong).
                        let payoutCell;
                        if (is1175) {
                            payoutCell = b.confirmed ? '<span style="color:var(--gold)">' + processingText + '</span>' : '<span style="color:var(--text-secondary)">' + pendingText + '</span>';
                        } else if (safeTxid) {
                            payoutCell = `<a href="https://explorer.bch2.org/tx/${safeTxid}" target="_blank" rel="noopener noreferrer" class="hash-link" style="color:var(--bch-green)">${truncateHash(safeTxid, 6, 4)}</a>`;
                        } else if (b.confirmed) {
                            payoutCell = '<span style="color:var(--gold)">' + processingText + '</span>';
                        } else {
                            payoutCell = '<span style="color:var(--text-secondary)">' + pendingText + '</span>';
                        }
                        const reward = (b.reward != null ? b.reward : (is1175 ? 0 : 50));
                        if (is1175) { esfReward += reward; esfCount++; } else { bch2Reward += reward; }
                        const rewardDisplay = formatBCH2(reward, is1175 ? 4 : 2) + (is1175 ? ' ESF' : ' BCH2');
                        return `
                        <tr>
                            <td>${coinBadge}</td>
                            <td style="color:${is1175 ? '#e0b341' : 'var(--gold)'}">${formatNumber(b.height)}</td>
                            <td>${blockCell}</td>
                            <td>${rewardDisplay}</td>
                            <td>${timeAgo(b.time)}</td>
                            <td><span class="status-badge ${b.confirmed ? 'status-confirmed' : 'status-pending'}">${b.confirmed ? confirmedText : pendingText}</span></td>
                            <td>${payoutCell}</td>
                        </tr>
                    `}).join("");
                    document.getElementById('blocksFound').textContent = formatNumber(minerBlocksCount);
                    let totalStr = 'Total: ' + formatBCH2(bch2Reward, 2) + ' BCH2';
                    if (esfCount > 0) totalStr += ' + ' + formatBCH2(esfReward, 4) + ' ESF';
                    document.getElementById('totalEarned').textContent = totalStr;
                }
                updateAvgEffort(data.blocks || []);
            } catch(e) {
                console.error("Failed to fetch blocks", e);
                tbody.innerHTML = '<tr><td colspan="7"><div class="error-state"><span class="error-icon">!</span><span data-i18n="p_error_load_blocks">' + (typeof PT !== 'undefined' && PT.p_error_load_blocks ? PT.p_error_load_blocks : 'Failed to load blocks') + '</span></div></td></tr>';
            }
        }

        async function fetchPayouts() {
            const tbody = document.getElementById("payoutsTable");
            try {
                const data = await apiFetch("/api/v1/miners/" + encodeURIComponent(minerAddress) + "/solo-payouts");
                document.getElementById("payoutCount").textContent = "(" + formatNumber(data.total || 0) + ")";
                document.getElementById("totalPaidAmount").textContent = formatNumber(data.totalPaid || 0);
                if (!data.payouts || data.payouts.length === 0) {
                    tbody.innerHTML = '<tr><td colspan="4"><div class="empty-state" data-i18n="p_solo_no_payouts">' + (typeof PT !== 'undefined' && PT.p_solo_no_payouts ? PT.p_solo_no_payouts : 'No payouts yet') + '</div></td></tr>';
                    return;
                }
                tbody.innerHTML = data.payouts.slice(0, 20).map(p => {
                    const safeTxid = isValidBlockHash(p.txid) ? p.txid : '';
                    return `
                    <tr>
                        <td>${safeTxid ? `<a href="https://explorer.bch2.org/tx/${safeTxid}" target="_blank" rel="noopener noreferrer" class="hash-link" style="color:var(--gold)">${truncateHash(safeTxid, 8, 4)}</a>` : (typeof PT !== 'undefined' && PT.p_status_pending ? PT.p_status_pending : 'Pending')}</td>
                        <td style="color:var(--bch-green)">${formatBCH2(p.amount || 0, 2)} BCH2</td>
                        <td>${formatNumber(p.blocks || 0)}</td>
                        <td>${timeAgo(p.paidAt)}</td>
                    </tr>
                `}).join("");
            } catch(e) {
                console.error("Failed to fetch payouts", e);
                tbody.innerHTML = '<tr><td colspan="4"><div class="error-state"><span class="error-icon">!</span><span data-i18n="p_error_load_payouts">' + (typeof PT !== 'undefined' && PT.p_error_load_payouts ? PT.p_error_load_payouts : 'Failed to load payouts') + '</span></div></td></tr>';
            }
        }

        function updateChart() {
            const ctx = document.getElementById('hashrateChart').getContext('2d');
            const now = Date.now();
            const labels = hashrateHistory.map((_, i) => {
                const time = new Date(now - (hashrateHistory.length - 1 - i) * 5 * 1000);
                return time.getHours() + ':' + time.getMinutes().toString().padStart(2, '0');
            });
            if (!hashrateChart) {
                hashrateChart = new Chart(ctx, {
                    type: 'line',
                    data: {
                        labels: labels,
                        datasets: [{
                            label: 'Hashrate (TH/s)',
                            data: hashrateHistory,
                            borderColor: '#f59e0b',
                            backgroundColor: 'rgba(245,158,11,0.1)',
                            fill: true,
                            tension: 0.4,
                            pointRadius: 0
                        }]
                    },
                    options: {
                        responsive: true,
                        maintainAspectRatio: false,
                        plugins: { legend: { display: false } },
                        scales: {
                            x: { display: true, ticks: { color: '#888', maxTicksLimit: 6 }, grid: { color: '#222' } },
                            y: { beginAtZero: true, ticks: { color: '#888' }, grid: { color: '#222' } }
                        }
                    }
                });
            } else {
                hashrateChart.data.labels = labels;
                hashrateChart.data.datasets[0].data = hashrateHistory;
                hashrateChart.update('none');
            }
        }

        (async function initDashboard() {
            if (!minerAddress) {
                try {
                    const cfg = await apiFetch('/api/v1/pool/config');
                    if (cfg && cfg.pool_address) minerAddress = cfg.pool_address;
                } catch (e) {}
                var _el = document.getElementById('minerAddress');
                if (_el) _el.textContent = minerAddress || '(configure payout address)';
            }
            fetchStats();
            fetchMinerData();
            fetchWorkers();
            fetchBlocks();
            fetchPayouts();
            setInterval(fetchStats, 30000);
            setInterval(fetchMinerData, 5000);
            setInterval(fetchWorkers, 10000);
            setInterval(fetchBlocks, 10000);
            setInterval(fetchPayouts, 30000);
        })();
