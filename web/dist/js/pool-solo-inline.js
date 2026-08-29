        // Whether /api/v1/pool/config has ever answered. Without this the page cannot
        // tell "you have not set a payout address" from "the API is down": minerAddress is
        // only ever learned from that endpoint, and apiFetch throws on a non-OK response,
        // so both arrive as an empty address. The app serves `web` from a separate nginx
        // container with start-only depends_on while `api` waits on postgres health, so an
        // unreachable API is the NORMAL view during startup, an update, or a crash-loop --
        // and telling a correctly configured user to set an address they already set
        // invites them to overwrite it from a Settings page that shows no error either.
        let configReachable = false;

        // Message for a table body or banner when we genuinely do not know the address.
        function noAddressNotice(forTable) {
            if (!configReachable) {
                return "Can't reach Forge Solo — the numbers below aren't live yet. " +
                       "If the app just started or updated, give it a minute.";
            }
            return forTable
                ? 'Set your payout address in Settings to begin.'
                : 'Set your payout address in Settings to mine.';
        }

        let minerAddress = new URLSearchParams(window.location.search).get('address') || decodeURIComponent(window.location.pathname.split('/solo/')[1] || '');
        // True when the address came from the URL rather than from this install's own
        // configuration. The stratum's authorized count is process-wide, so it only
        // describes the miner being viewed when that miner IS this install's payout
        // address -- which is the normal case for a solo dashboard, but not when
        // someone opens /solo?address=<someone else>.
        const addressFromUrl = !!minerAddress;
        document.getElementById('minerAddress').textContent = minerAddress;

        document.getElementById('copyAddressBtn').addEventListener('click', function() {
            copyText(minerAddress, this);
        });

        let hashrateChart;
        let hashrateHistory = [];
        let networkDiff = 0;   // 0 until a real value arrives; last-good is then held across polls
        let nodeSynced = false;   // set by updateStatusBanner; gates network stats during IBD
        let minerHashing = false; // set by fetchMinerData; true once a worker is actually hashing
        // Last /api/v1/mining-status payload and when it landed. The Workers tile needs the
        // stratum's LIVE connection count: "online" in the miner payload means "submitted a
        // share in the last 5 minutes", so an unplugged rig left the tile reading 1 for five
        // minutes underneath a banner correctly saying no miner was connected -- two true
        // statements that read as a contradiction on one screen.
        let lastMiningStatus = null;
        let lastMiningStatusAt = 0;
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

        // Status banner: shows "set your payout address" or live node-sync progress,
        // so a fresh install (empty address / still-syncing node) reads as a clear state
        // instead of a frozen spinner. Injected here so no HTML edit is required.
        (function ensureBanner(){
            if (document.getElementById('syncBanner')) return;
            var b = document.createElement('div');
            b.id = 'syncBanner';
            b.style.cssText = 'display:none;margin:0 0 16px;padding:11px 15px;border-radius:8px;background:rgba(224,179,65,0.12);color:#e0b341;border:1px solid rgba(224,179,65,0.35);font-size:14px;line-height:1.45;text-align:center';
            var dash = document.querySelector('.container.dashboard') || document.body;
            dash.insertBefore(b, dash.firstChild);
        })();

        // Reachability banner: sits under the status banner, because "nobody outside can reach
        // your node" is something to be TOLD, not something to find by scrolling to a card.
        // Separate from syncBanner on purpose -- that one cycles through mining states, and a
        // port that is closed stays closed through every one of them.
        (function ensureReachBanner(){
            if (document.getElementById('reachBanner')) return;
            var b = document.createElement('div');
            b.id = 'reachBanner';
            b.style.cssText = 'display:none;margin:0 0 16px;padding:11px 15px;border-radius:8px;'
                + 'background:rgba(224,179,65,0.12);color:#e0b341;border:1px solid rgba(224,179,65,0.35);'
                + 'font-size:0.92rem;line-height:1.55;';
            var sync = document.getElementById('syncBanner');
            if (sync && sync.parentNode) sync.parentNode.insertBefore(b, sync.nextSibling);
            else (document.querySelector('.container.dashboard') || document.body).appendChild(b);
        })();

        // The stratum address to tell the user to point a miner at. This page is served
        // from the same host as the stratum, so its own hostname is the right answer --
        // the old copy hardcoded "this PC: 127.0.0.1:3333 · a Bitaxe: your PC LAN IP",
        // which is wrong for the Umbrel this app ships as, and disagreed with Settings
        // and the README (both of which say <your-umbrel-ip>).
        function stratumHostHint() {
            var host = (window.location && window.location.hostname) || 'your-umbrel';
            return '3333 (stratum+tcp://' + host + ':3333)';
        }

        // How many workers are attached RIGHT NOW.
        //
        // The stratum's authorized count is the live truth, but it is a process-wide figure,
        // so it is only used when the page is showing this install's own payout address (no
        // ?address= override). Falls back to the miner payload's recent-share count when the
        // status is missing or stale, so a mining-status outage degrades rather than reading
        // a confident zero.
        const MINING_STATUS_MAX_AGE_MS = 30000;
        function workersConnectedNow(data) {
            const fresh = lastMiningStatus
                && (Date.now() - lastMiningStatusAt) < MINING_STATUS_MAX_AGE_MS;
            if (fresh && !addressFromUrl && lastMiningStatus.authorized != null) {
                return Number(lastMiningStatus.authorized) || 0;
            }
            return Number((data && data.onlineWorkers) || 0);
        }

        function escapeHtml(v) {
            return String(v == null ? '' : v).replace(/[&<>"']/g, function (ch) {
                return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch];
            });
        }

        async function updateStatusBanner() {
            var el = document.getElementById('syncBanner');
            if (!el) return;
            // Pick up a payout address saved in Settings without needing a page reload.
            if (!minerAddress) {
                try {
                    const cfg = await apiFetch('/api/v1/pool/config');
                    configReachable = true;
                    if (cfg && cfg.pool_address) {
                        minerAddress = cfg.pool_address;
                        var a = document.getElementById('minerAddress');
                        if (a) a.textContent = minerAddress;
                    }
                } catch (e) {}
            }
            var msg = '', tone = 'gold';   // gold = needs attention, green = ready / mining
            try {
                const s = await apiFetch('/api/v1/node-status');
                nodeSynced = (s.status === 'synced');
                if (s.status === 'syncing') {
                    const pct = (s.progress != null ? (s.progress * 100) : 0);
                    msg = '⏳ <b>BCH2 node syncing — ' + pct.toFixed(2) + '%</b> (block ' + (Number(s.blocks) || 0) + ' / ' + (Number(s.headers) || 0) + '). You can mine once it reaches 100%.'
                        + '<div style="margin-top:7px;height:7px;background:rgba(255,255,255,0.18);border-radius:4px;overflow:hidden">'
                        + '<div style="height:100%;width:' + pct.toFixed(1) + '%;background:#0ac18e;transition:width .6s"></div></div>';
                    if (!minerAddress) msg += '<div style="height:8px"></div>⚙️ Meanwhile, set your <a href="/settings" style="color:inherit;font-weight:600;text-decoration:underline">payout address</a> in Settings.';
                } else if (s.status === 'offline') {
                    msg = '⏳ <b>Starting the BCH2 node…</b> first launch can take a minute.';
                } else if (!minerAddress) {
                    msg = '✅ <b>Node synced.</b> Now set your <a href="/settings" style="color:inherit;font-weight:600;text-decoration:underline">payout address</a> in Settings to start mining.';
                } else {
                    // Node synced and an address is set -- but neither fact proves the
                    // mining service is actually handing out work. Ask it. A miner that
                    // is connected and receiving nothing is the one failure that used to
                    // render here as a cheerful "ready to mine".
                    let ms = null;
                    try {
                        ms = await apiFetch('/api/v1/mining-status');
                        lastMiningStatus = ms;
                        lastMiningStatusAt = Date.now();
                    } catch (e) {
                        // Stale status must not keep driving the Workers tile.
                        lastMiningStatus = null;
                    }
                    if (ms && ms.reason === 'no_miners') {
                        // Not a fault: the node is synced and work is ready, there is
                        // simply nothing attached. Rendering this in the alarming
                        // "Not mining" style would cry wolf on an ordinary idle install,
                        // but it must NOT be dressed up as mining either -- the previous
                        // code said "a miner is connected" in exactly this state.
                        tone = 'gold';
                        msg = '⏸️ <b>No miner connected.</b> Node synced and work is ready — point a miner at <b>port ' + stratumHostHint() + '</b>.';
                    } else if (ms && ms.mining === false && ms.message) {
                        // escapeHtml: ms.message can carry the node's raw JSON-RPC error
                        // text, which is the one dynamic string on this page that does not
                        // originate here.
                        msg = '⚠️ <b>Not mining.</b> ' + escapeHtml(ms.message);
                        // The detail line must agree with the message above it. "receiving no
                        // work" is only true when the node has stopped producing jobs; appending
                        // it to no_shares -- whose message says the miner IS receiving work and
                        // not submitting -- contradicted itself inside one banner. Seen on
                        // mainnet: "connected and receiving work but has not submitted an
                        // accepted share recently. 1 miner(s) connected and receiving no work."
                        if (ms.reason === 'miners_refused' && ms.connections > 0) {
                            msg += '<div style="margin-top:6px">' + Number(ms.connections) + ' connection(s), 0 authorized.</div>';
                        } else if (ms.connections > 0 && (ms.reason === 'stale_template' || ms.reason === 'no_template_yet')) {
                            msg += '<div style="margin-top:6px">' + Number(ms.connections) + ' miner(s) connected, but the node is not producing work for them.</div>';
                        } else if (ms.connections > 0 && ms.reason === 'no_shares') {
                            msg += '<div style="margin-top:6px">' + Number(ms.connections) + ' miner(s) connected and receiving work.</div>';
                        } else if (ms.connections > 0) {
                            msg += '<div style="margin-top:6px">' + Number(ms.connections) + ' miner(s) connected.</div>';
                        }
                    } else if (ms && ms.mining === true && Number(ms.authorized) > 0) {
                        tone = 'green';
                        msg = '⛏️ <b>Mining</b> — node synced, ' + Number(ms.authorized) + ' miner(s) authorized and submitting shares. Good luck!';
                    } else if (minerHashing) {
                        // Reached only when mining-status is unavailable. All this branch
                        // actually knows is that a worker submitted a share recently --
                        // it says nothing about connections, so it must not claim one.
                        tone = 'green';
                        msg = '⛏️ <b>Mining</b> — a worker is submitting shares. Good luck!';
                    } else {
                        tone = 'green';
                        msg = '✅ <b>Node synced — ready to mine.</b> Point a miner at <b>port ' + stratumHostHint() + '</b>.';
                    }
                }
            } catch (e) {
                if (!minerAddress) msg = (configReachable ? '⚙️ ' : '⚠️ ') + noAddressNotice(false);
            }
            if (!msg) { el.style.display = 'none'; return; }
            var c = (tone === 'green')
                ? ['rgba(10,193,142,0.12)', '#0ac18e', 'rgba(10,193,142,0.35)']
                : ['rgba(224,179,65,0.12)', '#e0b341', 'rgba(224,179,65,0.35)'];
            el.style.background = c[0]; el.style.color = c[1]; el.style.borderColor = c[2];
            el.innerHTML = msg;
            el.style.display = 'block';
        }

        async function fetchStats() {
            try {
                const data = await apiFetch('/api/v1/stats');
                // Keep the last good network difficulty. A transient node-RPC hiccup makes the API
                // return 0 for a poll; never clobber a good tile with 0 (that's the "tiles flash 0" bug).
                if (data.networkDifficulty > 0) networkDiff = data.networkDifficulty;
                // Until the node reaches the chain tip, getdifficulty/getnetworkhashps report the
                // value at the CURRENT (low) sync height — wildly off — so show "syncing…" instead.
                if (nodeSynced) {
                    // Only update a tile when this poll actually carried a value; otherwise leave the
                    // last good reading on screen instead of blanking it to 0 / "--".
                    if (networkDiff > 0) document.getElementById('networkDiff').textContent = formatDiff(networkDiff);
                    if (data.networkHashrate > 0) document.getElementById('networkHashrate').textContent = formatHashrate(data.networkHashrate);
                } else {
                    document.getElementById('networkDiff').textContent = 'syncing…';
                    document.getElementById('networkHashrate').textContent = 'syncing…';
                }
            } catch(e) {
                console.error('Failed to fetch stats', e);
            }
        }

        async function fetchMinerData() {
            if (!minerAddress) return;   // no address yet -> don't hit /miners/ (404 spam)
            try {
                const data = await apiFetch('/api/v1/miners/' + encodeURIComponent(minerAddress));
                // Solo-only home app: always render this dashboard.
                document.getElementById('matureBalance').textContent = formatBCH2(data.matureBalance || 0, 2);
                document.getElementById('immatureBalance').textContent = formatBCH2(data.immatureBalance || 0, 2);
                document.getElementById('hashrate5m').textContent = formatHashrate((data.hashrate5m || 0) * 1e12);
                document.getElementById('hashrate60m').textContent = formatHashrate((data.hashrate60m || 0) * 1e12);
                // Never data.workers: that counts every worker ever seen. The stats manager
                // never deletes from its map (MarkStaleWorkersOffline only flips a bool),
                // so it stays >= 1 for the life of the stratum process --
                // an unplugged rig, a rig moved to another pool, a renamed worker or
                // rotating rental worker names all left the tile reading 1 next to a
                // hashrate of 0, while the same app answered 0 on /api/v1/stats.
                //
                // Prefer the stratum's LIVE authorized count over data.onlineWorkers.
                // "online" there means "submitted a share in the last 5 minutes", so a rig
                // that is switched off still counts for five minutes -- underneath a banner
                // correctly reporting no miner connected. Both were true; together they read
                // as a contradiction, and on a solo dashboard "Workers" means what is
                // attached right now.
                document.getElementById('workers').textContent = formatNumber(workersConnectedNow(data));
                document.getElementById('validShares').textContent = formatNumber(data.validShares || 0);
                // roundShares, NOT validShares. validShares is all-time and is never reset --
                // it is the same field the "Valid Shares" tile uses, so the two tiles were
                // byte-identical always, and this one sat inside the Round Effort card next
                // to a Current Effort and Best Difficulty that the round reset HAD cleared.
                document.getElementById('roundShares').textContent = formatNumber(data.roundShares || 0);
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
                // onlineWorkers, NOT workers: `workers` counts every worker ever seen and is
                // never pruned, so it stays >= 1 for the life of the stratum process. Using it
                // made the "a miner is connected" banner latch on permanently after a rig was
                // unplugged -- and because the accurate rung above requires authorized > 0,
                // this branch is only ever REACHED when nothing is connected, so it could
                // never once describe a live miner correctly.
                minerHashing = hashrate > 0 || (data.onlineWorkers || 0) > 0;
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

        // Connect & Network -------------------------------------------------------------
        //
        // Two different audiences, and conflating them is what sends a paid rental order to a
        // port nothing is listening on:
        //
        //   * Your own hardware reaches this box on the LAN, so the page's own hostname is the
        //     right answer -- the same reasoning as stratumHostHint().
        //   * A marketplace dials in from the internet, so it needs the PUBLIC address and the
        //     rental port, and only if a rental listener actually came up. The Windows build
        //     ships that listener disabled, which is why the port comes from the live mining
        //     status rather than from a constant here.
        //
        // Inbound peer counts are the honest test of a P2P forward. Outbound peers prove
        // nothing -- a node behind a closed port still makes plenty. One inbound peer means
        // somebody out there reached you, which no self-reported address can establish.
        function connPill(known, reachable, okText, closedText) {
            const el = document.createElement('span');
            if (!known) {
                el.className = 'conn-pill unknown';
                el.textContent = 'node unreachable';
            } else if (reachable) {
                el.className = 'conn-pill ok';
                el.textContent = okText;
            } else {
                el.className = 'conn-pill closed';
                el.textContent = closedText;
            }
            return el;
        }

        function connPortRow(name, info) {
            const row = document.createElement('div');
            row.className = 'conn-port';

            const label = document.createElement('span');
            label.textContent = name;
            row.appendChild(label);

            const port = document.createElement('code');
            port.textContent = String(info.port);
            row.appendChild(port);

            row.appendChild(connPill(info.known, info.reachable,
                'reachable · ' + info.inbound + ' inbound',
                'no inbound peers'));

            if (info.known) {
                const peers = document.createElement('span');
                peers.style.color = 'var(--text-secondary)';
                peers.textContent = info.peers + ' peers connected';
                row.appendChild(peers);
            }
            return row;
        }

        async function fetchConnectivity() {
            let c;
            try {
                c = await apiFetch('/api/v1/connectivity');
            } catch (e) {
                return; // leave the last good values on screen rather than blanking them
            }

            const host = (window.location && window.location.hostname) || 'your-node';
            const local = document.getElementById('connLocal');
            if (local) local.textContent = 'stratum+tcp://' + host + ':' + (c.stratumPort || 3333);

            // Which port a marketplace should dial. The dedicated rental listener exists to
            // give an aggregated order its own high difficulty floor, but it is not always
            // running -- the Windows build ships it off -- and in that case the main port is
            // the honest answer rather than nothing at all.
            const group = document.getElementById('connRentalGroup');
            const rentalPort = (lastMiningStatus && Number(lastMiningStatus.rental_port))
                || Number(c.stratumPort) || 3333;
            if (group) {
                group.hidden = false;
                const value = document.getElementById('connRental');
                const note = document.getElementById('connRentalNote');
                if (c.publicIp) {
                    value.textContent = 'stratum+tcp://' + c.publicIp + ':' + rentalPort;
                    note.textContent = 'Forward port ' + rentalPort + ' to this machine in your router, '
                        + 'or the order will pay for hashrate that never reaches you.';
                } else {
                    value.textContent = 'Public address not known yet';
                    note.textContent = 'Your node learns its public address from the peers that reach it. '
                        + 'While this is blank, port ' + (c.bch2 && c.bch2.port ? c.bch2.port : 8339)
                        + ' is almost certainly not forwarded either \u2014 and a rental would not reach '
                        + 'you on port ' + rentalPort + ' until you forward that too.';
                }
            }

            const p2p = document.getElementById('connP2P');
            if (p2p && c.bch2 && c.aux1175) {
                p2p.textContent = '';
                p2p.appendChild(connPortRow('BCH2', c.bch2));
                p2p.appendChild(connPortRow('1175', c.aux1175));
            }

            renderReachBanner(c, rentalPort);
        }

        // Only speak up when there is something to do about it.
        //
        // Silent when every port is open (the card below already shows green) and silent when
        // a node could not be consulted -- telling someone their forward is broken because
        // their node was restarting is how a warning gets ignored for the one time it matters.
        function renderReachBanner(c, rentalPort) {
            const el = document.getElementById('reachBanner');
            if (!el) return;

            const closed = [];
            if (c.bch2 && c.bch2.known && !c.bch2.reachable) closed.push({ name: 'BCH2', port: c.bch2.port });
            if (c.aux1175 && c.aux1175.known && !c.aux1175.reachable) closed.push({ name: '1175', port: c.aux1175.port });

            if (!closed.length) { el.style.display = 'none'; return; }

            const ports = closed.map(p => 'TCP ' + p.port + ' (' + p.name + ')').join(' and ');
            // Say only what is true. One chain being unreachable while the other is fine is a
            // different sentence from both being shut, and a banner that overstates the
            // problem is one the reader learns to skip.
            const total = [c.bch2, c.aux1175].filter(n => n && n.known).length;
            const allClosed = closed.length === total;
            const parts = [];
            parts.push(allClosed
                ? '<b>Nothing on the internet can reach your node.</b> Forward ' + ports
                    + ' to this machine in your router.'
                : '<b>Your ' + closed.map(p => p.name).join(' and ') + ' node is not reachable '
                    + 'from the internet.</b> Forward ' + ports + ' to this machine in your router.');
            parts.push((allClosed
                    ? 'Your node is connected out to peers, but none have connected in. '
                    : 'It is connected out to peers, but none have connected in on that port. ')
                + 'Accepting inbound peers is what keeps the network reachable instead of leaning '
                + 'on a handful of well-connected machines \u2014 it is not required to mine.');
            parts.push('<b>Mining from outside your network</b>, including rented hashrate, needs '
                + 'TCP ' + rentalPort + ' forwarded as well \u2014 otherwise an order pays for '
                + 'hashrate that never arrives.');
            // This clears on proof -- an inbound peer -- not on the router rule being saved,
            // and the first peer can take a while to find you. Without saying so, the obvious
            // reading of a warning that survives the fix is that the fix did not work.
            parts.push('<i>Already forwarded? This clears itself once the first peer connects '
                + 'in, which can take a few minutes. Nothing to reload.</i>');

            el.innerHTML = '\uD83D\uDD0C ' + parts.join('<br><br>');
            el.style.display = 'block';
        }

        async function fetchWorkers() {
            const tbody = document.getElementById('workersTable');
            if (!minerAddress) { tbody.innerHTML = '<tr><td colspan="6"><div class="empty-state">' + escapeHtml(noAddressNotice(true)) + '</div></td></tr>'; return; }
            try {
                const data = await apiFetch('/api/v1/miners/' + encodeURIComponent(minerAddress) + '/workers');
                if (!data.workers || data.workers.length === 0) {
                    tbody.innerHTML = '<tr><td colspan="6"><div class="empty-state" data-i18n="p_solo_no_workers">' + (typeof PT !== 'undefined' && PT.p_solo_no_workers ? PT.p_solo_no_workers : 'No workers connected') + '</div></td></tr>';
                    return;
                }
                tbody.innerHTML = data.workers.map(w => `
                    <tr>
                        <td><span class="status-dot ${w.online ? 'online' : 'offline'}" aria-hidden="true"></span>${sanitizeHTML(w.name || 'default')}${w.online ? '' : ' <span style="color:var(--text-secondary);font-size:0.85em">(offline)</span>'}</td>
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
            if (!minerAddress) { tbody.innerHTML = '<tr><td colspan="7"><div class="empty-state">' + escapeHtml(noAddressNotice(true)) + '</div></td></tr>'; return; }
            try {
                const data = await apiFetch('/api/v1/miners/' + encodeURIComponent(minerAddress) + '/solo-blocks');
                if (!data.blocks || data.blocks.length === 0) {
                    tbody.innerHTML = '<tr><td colspan="7"><div class="empty-state" data-i18n="p_solo_no_blocks">' + (typeof PT !== 'undefined' && PT.p_solo_no_blocks ? PT.p_solo_no_blocks : 'No blocks found yet. Keep mining!') + '</div></td></tr>';
                    minerBlocksCount = 0;
                    document.getElementById('blocksFound').textContent = '0';
                    document.getElementById('totalEarned').textContent = 'Total: 0 BCH2';
                } else {
                    const sorted = data.blocks.slice().sort((a, b) => (b.time || 0) - (a.time || 0));
                    // data.total is the BCH2 count the API already computed. sorted.length
                    // merged in the merge-mined 1175 rows, so the tile read 10 while the
                    // same page's own workers table and /api/v1/stats both said 7.
                    minerBlocksCount = (data.total != null) ? data.total : sorted.filter(b => b.coin !== '1175').length;
                    var confirmedText = typeof PT !== 'undefined' && PT.p_status_confirmed ? PT.p_status_confirmed : 'Confirmed';
                    var pendingText = typeof PT !== 'undefined' && PT.p_status_pending ? PT.p_status_pending : 'Pending';
                    var processingText = typeof PT !== 'undefined' && PT.p_status_processing ? PT.p_status_processing : 'Processing';
                    let bch2Reward = 0, esfReward = 0, esfCount = 0;
                    // Sum over EVERY row the API returned, not just the 20 rendered below.
                    // The total was accumulated inside the .slice(0,20).map(), while the
                    // count printed beside it is the server-side figure -- so past 20 rows
                    // (about 10 BCH2 blocks, since merge-mining adds a 1175 row to each)
                    // the page showed a total that silently stopped growing.
                    const shownLimit = 20;
                    for (const b of sorted) {
                        if (b.status === 'orphaned') continue;   // paid nothing
                        const r = (b.reward != null ? b.reward : (b.coin === '1175' ? 0 : 50));
                        if (b.coin === '1175') { esfReward += r; esfCount++; } else { bch2Reward += r; }
                    }
                    tbody.innerHTML = sorted.slice(0, shownLimit).map(b => {
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
                        // An orphaned block paid nothing: the chain discarded it. It used to
                        // render as found and "Paid by coinbase" and count toward the totals,
                        // because the renderer never looked at b.status and treated every
                        // 1175 row as paid unconditionally.
                        const isOrphaned = b.status === 'orphaned';
                        // Solo rewards are paid by the block's OWN coinbase on both chains, so
                        // there is no payout transaction to wait for and no "Processing" state
                        // to pass through. Without this every solo block sat at Pending, then
                        // Processing, forever -- next to a Total Paid that already counted it.
                        const paidByCoinbase = !isOrphaned && (b.payoutTxid === 'coinbase-direct' || is1175);
                        if (isOrphaned) {
                            payoutCell = '<span style="color:var(--red)" title="This block was superseded on the chain and paid nothing">Orphaned</span>';
                        } else if (paidByCoinbase) {
                            payoutCell = '<span style="color:var(--bch-green)" title="Paid directly by this block\u2019s coinbase — there is no separate payout transaction">Paid by coinbase</span>';
                        } else if (is1175) {
                            payoutCell = b.confirmed ? '<span style="color:var(--gold)">' + processingText + '</span>' : '<span style="color:var(--text-secondary)">' + pendingText + '</span>';
                        } else if (safeTxid) {
                            payoutCell = `<a href="https://explorer.bch2.org/tx/${safeTxid}" target="_blank" rel="noopener noreferrer" class="hash-link" style="color:var(--bch-green)">${truncateHash(safeTxid, 6, 4)}</a>`;
                        } else if (b.confirmed) {
                            payoutCell = '<span style="color:var(--gold)">' + processingText + '</span>';
                        } else {
                            payoutCell = '<span style="color:var(--text-secondary)">' + pendingText + '</span>';
                        }
                        const reward = (b.reward != null ? b.reward : (is1175 ? 0 : 50));
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
                    let totalStr = 'Total: ' + formatBCH2(bch2Reward, 8) + ' BCH2';
                    if (esfCount > 0) totalStr += ' + ' + formatBCH2(esfReward, 8) + ' ESF';
                    if (sorted.length > shownLimit) totalStr += ' (latest ' + shownLimit + ' shown)';
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
            if (!minerAddress) { tbody.innerHTML = '<tr><td colspan="4"><div class="empty-state">' + escapeHtml(noAddressNotice(true)) + '</div></td></tr>'; return; }
            try {
                const data = await apiFetch("/api/v1/miners/" + encodeURIComponent(minerAddress) + "/solo-payouts");
                document.getElementById("payoutCount").textContent = "(" + formatNumber(data.total || 0) + ")";
                // formatBCH2, not formatNumber. formatNumber is toLocaleString(), which caps
                // at 3 fraction digits and ROUNDS: 200.99999999 rendered as "201". On mainnet
                // a coinbase is subsidy plus fees, so this figure essentially always has 8
                // decimals -- and it is the one number a user checks against their wallet.
                document.getElementById("totalPaidAmount").textContent = formatBCH2(data.totalPaid || 0, 8);
                if (!data.payouts || data.payouts.length === 0) {
                    tbody.innerHTML = '<tr><td colspan="4"><div class="empty-state" data-i18n="p_solo_no_payouts">' + (typeof PT !== 'undefined' && PT.p_solo_no_payouts ? PT.p_solo_no_payouts : 'No payouts yet') + '</div></td></tr>';
                    return;
                }
                tbody.innerHTML = data.payouts.slice(0, 20).map(p => {
                    const safeTxid = isValidBlockHash(p.txid) ? p.txid : '';
                    // A solo reward is paid by the block's own coinbase, so its "txid" is the
                    // literal 'coinbase-direct' rather than a 64-hex hash. Falling through to
                    // the hash check rendered every settled solo payout as "Pending" forever,
                    // directly beside a "Total Paid" that already counted it.
                    const paidByCoinbase = p.txid === 'coinbase-direct';
                    // An orphaned block's reward is VOID -- it was never paid and never will
                    // be. It reaches here with confirmed=false and no real txid, exactly like
                    // a payout still on its way, so without reading the status it rendered as
                    // "Pending <amount>": a payment that will never arrive, shown as one that
                    // is coming. The blocks table on this same page already says "Orphaned".
                    const orphaned = p.status === 'orphaned';
                    const statusCell = orphaned
                        ? '<span style="color:var(--red)" title="This block was superseded on the chain and paid nothing">Orphaned</span>'
                        : (safeTxid
                            ? `<a href="https://explorer.bch2.org/tx/${safeTxid}" target="_blank" rel="noopener noreferrer" class="hash-link" style="color:var(--gold)">${truncateHash(safeTxid, 8, 4)}</a>`
                            : (paidByCoinbase
                                ? '<span style="color:var(--bch-green)">Paid by coinbase</span>'
                                : (typeof PT !== 'undefined' && PT.p_status_pending ? PT.p_status_pending : 'Pending')));
                    const amountStyle = orphaned
                        ? 'color:var(--text-secondary);text-decoration:line-through'
                        : 'color:var(--bch-green)';
                    return `
                    <tr>
                        <td>${statusCell}</td>
                        <td style="${amountStyle}">${formatBCH2(p.amount || 0, 2)} BCH2</td>
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
                    configReachable = true;
                    if (cfg && cfg.pool_address) minerAddress = cfg.pool_address;
                } catch (e) {}
                var _el = document.getElementById('minerAddress');
                if (_el) _el.textContent = minerAddress || (configReachable ? '(configure payout address)' : '(unavailable — cannot reach Forge Solo)');
            }
            await updateStatusBanner();
            fetchStats();
            fetchMinerData();
            fetchWorkers();
            fetchConnectivity();
            fetchBlocks();
            fetchPayouts();
            setInterval(updateStatusBanner, 5000);
            setInterval(fetchConnectivity, 30000);
            setInterval(fetchStats, 30000);
            setInterval(fetchMinerData, 5000);
            setInterval(fetchWorkers, 10000);
            setInterval(fetchBlocks, 10000);
            setInterval(fetchPayouts, 30000);
        })();
