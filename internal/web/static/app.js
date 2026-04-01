(function () {
    "use strict";

    var API = "/api/v1";
    var PAGE_SIZE = 50;
    var state = { view: "dashboard", page: 1, searchPage: 1, searchQuery: "", apiKey: "", filter: {} };

    // ---- Theme ----
    function initTheme() {
        if (localStorage.getItem("msgvault-theme") === "light") {
            document.documentElement.setAttribute("data-theme", "light");
        }
    }
    function toggleTheme() {
        var isLight = document.documentElement.getAttribute("data-theme") === "light";
        if (isLight) document.documentElement.removeAttribute("data-theme");
        else document.documentElement.setAttribute("data-theme", "light");
        localStorage.setItem("msgvault-theme", isLight ? "dark" : "light");
    }

    // ---- API ----
    function apiHeaders() {
        var h = { "Content-Type": "application/json" };
        if (state.apiKey) h["X-API-Key"] = state.apiKey;
        return h;
    }
    function apiFetch(path) {
        return fetch(API + path, { headers: apiHeaders() }).then(function (r) {
            if (!r.ok) throw new Error("HTTP " + r.status);
            return r.json();
        });
    }

    // ---- Formatting ----
    function formatDate(iso) {
        if (!iso) return "";
        var d = new Date(iso);
        var now = new Date();
        var day = String(d.getDate()).padStart(2, "0");
        var month = String(d.getMonth() + 1).padStart(2, "0");
        var hours = String(d.getHours()).padStart(2, "0");
        var mins = String(d.getMinutes()).padStart(2, "0");
        // Today: just time
        if (d.toDateString() === now.toDateString()) return hours + ":" + mins;
        // This year: day/month
        if (d.getFullYear() === now.getFullYear()) return day + "/" + month;
        return day + "/" + month + "/" + d.getFullYear();
    }
    function formatDateFull(iso) {
        if (!iso) return "-";
        var d = new Date(iso);
        var day = String(d.getDate()).padStart(2, "0");
        var month = String(d.getMonth() + 1).padStart(2, "0");
        return day + "/" + month + "/" + d.getFullYear() + " " +
            String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");
    }
    function formatSize(bytes) {
        if (!bytes) return "0 o";
        if (bytes < 1024) return bytes + " o";
        if (bytes < 1048576) return (bytes / 1024).toFixed(1) + " Ko";
        if (bytes < 1073741824) return (bytes / 1048576).toFixed(1) + " Mo";
        return (bytes / 1073741824).toFixed(1) + " Go";
    }
    function esc(text) {
        var el = document.createElement("span");
        el.textContent = text || "";
        return el.innerHTML;
    }
    function senderName(msg) {
        if (msg.from_name) return msg.from_name;
        if (msg.from_email) return msg.from_email.split("@")[0];
        if (msg.from) return msg.from.split("@")[0];
        return "?";
    }
    function senderEmail(msg) {
        return msg.from_email || msg.from || "";
    }

    // ---- Render message item ----
    function renderMsg(msg) {
        var labels = "";
        var msgLabels = msg.labels || [];
        if (msgLabels.length > 0) {
            labels = '<div class="msg-labels">' +
                msgLabels.slice(0, 3).map(function (l) {
                    return '<span class="label-badge" data-label="' + esc(l) + '">' + esc(l) + '</span>';
                }).join("") +
                (msgLabels.length > 3 ? '<span class="label-badge">+' + (msgLabels.length - 3) + '</span>' : "") +
                '</div>';
        }
        var attach = msg.has_attachments ? '<span class="msg-attach" title="Pieces jointes">&#128206;</span> ' : "";
        return '<div class="message-item" data-id="' + msg.id + '">' +
            '<div class="msg-top">' +
                '<span class="msg-from-name">' + esc(senderName(msg)) + '</span> ' +
                attach +
                '<span class="msg-subject">' + esc(msg.subject || "(sans sujet)") + '</span>' +
            '</div>' +
            '<div class="msg-date">' + formatDate(msg.sent_at) + '</div>' +
            '<div class="msg-snippet">' + esc(msg.snippet || "") + '</div>' +
            labels +
            '</div>';
    }

    function renderPagination(el, page, total, onPage) {
        var pages = Math.max(1, Math.ceil(total / PAGE_SIZE));
        el.innerHTML =
            '<button ' + (page <= 1 ? "disabled" : "") + ' data-p="' + (page - 1) + '">&#8249; Precedent</button>' +
            '<span class="page-info">' + page + ' / ' + pages + '</span>' +
            '<button ' + (page >= pages ? "disabled" : "") + ' data-p="' + (page + 1) + '">Suivant &#8250;</button>';
        el.querySelectorAll("button").forEach(function (b) {
            b.addEventListener("click", function () {
                var p = parseInt(b.getAttribute("data-p"));
                if (p >= 1 && p <= pages) onPage(p);
            });
        });
    }

    function bindClicks(container) {
        container.querySelectorAll(".message-item").forEach(function (item) {
            item.addEventListener("click", function (e) {
                // If clicking a label badge, filter by that label instead.
                if (e.target.classList.contains("label-badge")) {
                    var label = e.target.getAttribute("data-label");
                    if (label) { filterByLabel(label); return; }
                }
                loadDetail(item.getAttribute("data-id"));
            });
        });
    }

    // ---- Views ----
    function switchView(name) {
        state.view = name;
        document.querySelectorAll(".view").forEach(function (v) { v.classList.remove("active"); });
        var el = document.getElementById("view-" + name);
        if (el) el.classList.add("active");
        // Update sidebar active state.
        document.querySelectorAll(".sidebar-item[data-view]").forEach(function (i) {
            i.classList.toggle("active", i.getAttribute("data-view") === name);
        });
    }

    // ---- Dashboard ----
    function loadDashboard() {
        switchView("dashboard");
        var statsEl = document.getElementById("dashboard-stats");
        var recentEl = document.getElementById("dashboard-recent");

        apiFetch("/stats").then(function (d) {
            statsEl.innerHTML =
                statCard(d.total_messages || 0, "Messages") +
                statCard(d.total_accounts || 0, "Comptes") +
                statCard(d.total_labels || 0, "Labels") +
                statCard(d.total_attachments || 0, "Pieces jointes") +
                statCard(formatSize(d.database_size_bytes), "Taille");
        }).catch(function () { statsEl.innerHTML = '<div class="empty">API indisponible</div>'; });

        apiFetch("/messages?page=1&page_size=15").then(function (d) {
            var msgs = d.messages || [];
            if (!msgs.length) { recentEl.innerHTML = '<div class="empty">Aucun message</div>'; return; }
            recentEl.innerHTML = msgs.map(renderMsg).join("");
            bindClicks(recentEl);
        }).catch(function () {});
    }
    function statCard(v, l) {
        return '<div class="stat-card"><div class="stat-value">' + v + '</div><div class="stat-label">' + l + '</div></div>';
    }

    // ---- Messages ----
    function loadMessages(page, filter) {
        state.page = page || 1;
        state.filter = filter || {};
        switchView("messages");

        var list = document.getElementById("message-list");
        var pag = document.getElementById("pagination");
        var title = document.getElementById("messages-title");
        var countEl = document.getElementById("messages-count");

        list.innerHTML = '<div class="loading">Chargement...</div>';

        var params = "page=" + state.page + "&page_size=" + PAGE_SIZE;
        var endpoint = "/messages";

        // Build filter URL.
        if (filter && filter.label) {
            endpoint = "/messages/filter";
            params = "label=" + encodeURIComponent(filter.label) + "&limit=" + PAGE_SIZE + "&offset=" + ((state.page - 1) * PAGE_SIZE);
            title.textContent = "Label : " + filter.label;
        } else if (filter && filter.sender) {
            endpoint = "/messages/filter";
            params = "sender=" + encodeURIComponent(filter.sender) + "&limit=" + PAGE_SIZE + "&offset=" + ((state.page - 1) * PAGE_SIZE);
            title.textContent = "De : " + filter.sender;
        } else if (filter && filter.domain) {
            endpoint = "/messages/filter";
            params = "domain=" + encodeURIComponent(filter.domain) + "&limit=" + PAGE_SIZE + "&offset=" + ((state.page - 1) * PAGE_SIZE);
            title.textContent = "Domaine : " + filter.domain;
        } else {
            title.textContent = "Tous les messages";
        }

        apiFetch(endpoint + "?" + params).then(function (d) {
            var msgs = d.messages || [];
            var total = d.total || msgs.length;
            countEl.textContent = total > 0 ? total + " messages" : "";
            if (!msgs.length) { list.innerHTML = '<div class="empty">Aucun message</div>'; pag.innerHTML = ""; return; }
            list.innerHTML = msgs.map(renderMsg).join("");
            if (total > PAGE_SIZE) renderPagination(pag, state.page, total, function (p) { loadMessages(p, filter); });
            else pag.innerHTML = "";
            bindClicks(list);
        }).catch(function (e) {
            list.innerHTML = '<div class="empty">Erreur : ' + esc(e.message) + '</div>';
            pag.innerHTML = "";
        });
    }

    // ---- Search ----
    function doSearch(q, page) {
        if (!q) return;
        state.searchQuery = q;
        state.searchPage = page || 1;
        switchView("search");

        var list = document.getElementById("search-results");
        var pag = document.getElementById("search-pagination");
        var title = document.getElementById("search-title");
        title.textContent = 'Resultats pour "' + q + '"';
        list.innerHTML = '<div class="loading">Recherche...</div>';

        // Detect structured filters.
        var m;
        var url;
        var offset = (state.searchPage - 1) * PAGE_SIZE;

        m = q.match(/^label:(.+)$/i);
        if (m) { filterByLabel(m[1]); return; }
        m = q.match(/^from:(.+)$/i);
        if (m) { loadMessages(1, { sender: m[1] }); return; }
        m = q.match(/^domain:(.+)$/i);
        if (m) { loadMessages(1, { domain: m[1] }); return; }

        url = "/search?q=" + encodeURIComponent(q) + "&page_size=" + PAGE_SIZE + "&page=" + state.searchPage;

        apiFetch(url).then(function (d) {
            var msgs = d.messages || [];
            var total = d.total || msgs.length;
            if (!msgs.length) { list.innerHTML = '<div class="empty">Aucun resultat</div>'; pag.innerHTML = ""; return; }
            list.innerHTML = msgs.map(renderMsg).join("");
            if (total > PAGE_SIZE) renderPagination(pag, state.searchPage, total, function (p) { doSearch(q, p); });
            else pag.innerHTML = '<span class="page-info">' + total + ' resultats</span>';
            bindClicks(list);
        }).catch(function (e) {
            list.innerHTML = '<div class="empty">Erreur : ' + esc(e.message) + '</div>';
            pag.innerHTML = "";
        });
    }

    // ---- Filter shortcuts ----
    function filterByLabel(label) { loadMessages(1, { label: label }); }
    function filterBySender(email) { loadMessages(1, { sender: email }); }

    // ---- Message detail ----
    function loadDetail(id) {
        var panel = document.getElementById("message-detail");
        panel.classList.remove("hidden");
        document.getElementById("detail-subject").textContent = "Chargement...";
        document.getElementById("detail-from").textContent = "";
        document.getElementById("detail-to").textContent = "";
        document.getElementById("detail-date").textContent = "";
        document.getElementById("detail-labels-row").innerHTML = "";
        document.getElementById("detail-attachments").innerHTML = "";
        document.getElementById("detail-body").textContent = "";

        apiFetch("/messages/" + id).then(function (msg) {
            document.getElementById("detail-subject").textContent = msg.subject || "(sans sujet)";

            var fromEl = document.getElementById("detail-from");
            var fromAddr = msg.from || msg.from_email || "";
            fromEl.textContent = fromAddr || "-";
            fromEl.onclick = function () { if (fromAddr) { filterBySender(fromAddr); panel.classList.add("hidden"); } };

            var toList = msg.to || [];
            document.getElementById("detail-to").textContent = toList.join(", ") || "-";

            document.getElementById("detail-date").textContent = formatDateFull(msg.sent_at);

            var labelsHtml = (msg.labels || []).map(function (l) {
                return '<span class="label-badge" data-label="' + esc(l) + '">' + esc(l) + '</span>';
            }).join(" ");
            var labelsRow = document.getElementById("detail-labels-row");
            labelsRow.innerHTML = labelsHtml;
            labelsRow.querySelectorAll(".label-badge").forEach(function (b) {
                b.addEventListener("click", function () {
                    filterByLabel(b.getAttribute("data-label"));
                    panel.classList.add("hidden");
                });
            });

            var atts = msg.attachments || [];
            if (atts.length) {
                document.getElementById("detail-attachments").innerHTML = atts.map(function (a) {
                    return '<span class="attachment-chip"><span class="att-icon">&#128206;</span> ' +
                        esc(a.filename || a.mime_type || "fichier") + ' (' + formatSize(a.size || a.size_bytes || 0) + ')</span>';
                }).join("");
            }

            // Render body: prefer HTML in sandboxed iframe, fallback to plain text.
            var bodyEl = document.getElementById("detail-body");
            var bodyHtml = msg.body_html || "";
            var bodyText = msg.body_text || msg.body || "";

            if (bodyHtml) {
                // Render HTML email in a sandboxed iframe for safety.
                bodyEl.innerHTML = "";
                var iframe = document.createElement("iframe");
                iframe.sandbox = "allow-same-origin";
                iframe.style.cssText = "width:100%;border:none;min-height:400px;background:#fff;border-radius:4px;";
                bodyEl.appendChild(iframe);
                iframe.addEventListener("load", function () {
                    var doc = iframe.contentDocument || iframe.contentWindow.document;
                    doc.open();
                    doc.write('<!DOCTYPE html><html><head><meta charset="utf-8"><style>body{font-family:sans-serif;font-size:14px;margin:1rem;color:#333;}</style></head><body>' + bodyHtml + '</body></html>');
                    doc.close();
                    // Auto-resize iframe to content height.
                    setTimeout(function () {
                        try { iframe.style.height = doc.body.scrollHeight + 40 + "px"; } catch (e) {}
                    }, 200);
                });
                // Trigger load.
                iframe.src = "about:blank";
            } else {
                bodyEl.textContent = bodyText || "(vide)";
            }
        }).catch(function (e) {
            document.getElementById("detail-subject").textContent = "Erreur";
            document.getElementById("detail-body").textContent = e.message;
        });
    }

    // ---- Sidebar ----
    function loadSidebar() {
        // Labels
        apiFetch("/aggregates?view_type=labels&sort=count&limit=30").then(function (d) {
            var rows = d.rows || [];
            var el = document.getElementById("sidebar-labels");
            document.getElementById("label-count").textContent = rows.length;
            if (!rows.length) { el.innerHTML = '<div class="sidebar-item" style="color:var(--text-dim)">Aucun</div>'; return; }
            el.innerHTML = rows.map(function (r) {
                return '<a href="#" class="sidebar-item" data-action="label" data-value="' + esc(r.key) + '">' +
                    '<span class="icon">&#9679;</span> ' + esc(r.key) +
                    '<span class="item-count">' + r.count + '</span></a>';
            }).join("");
            el.querySelectorAll("[data-action=label]").forEach(function (a) {
                a.addEventListener("click", function (e) { e.preventDefault(); filterByLabel(a.getAttribute("data-value")); });
            });
        }).catch(function () {});

        // Top senders
        apiFetch("/aggregates?view_type=senders&sort=count&limit=15").then(function (d) {
            var rows = d.rows || [];
            var el = document.getElementById("sidebar-senders");
            if (!rows.length) { el.innerHTML = '<div class="sidebar-item" style="color:var(--text-dim)">Aucun</div>'; return; }
            el.innerHTML = rows.map(function (r) {
                var name = r.key.split("@")[0];
                return '<a href="#" class="sidebar-item" data-action="sender" data-value="' + esc(r.key) + '">' +
                    '<span class="icon">&#128100;</span> ' + esc(name) +
                    '<span class="item-count">' + r.count + '</span></a>';
            }).join("");
            el.querySelectorAll("[data-action=sender]").forEach(function (a) {
                a.addEventListener("click", function (e) { e.preventDefault(); filterBySender(a.getAttribute("data-value")); });
            });
        }).catch(function () {});
    }

    // ---- Stats bar ----
    function loadStatsBar() {
        apiFetch("/stats").then(function (d) {
            document.getElementById("stats-bar").textContent =
                (d.total_messages || 0) + " emails  |  " + formatSize(d.database_size_bytes || 0);
        }).catch(function () {
            document.getElementById("stats-bar").textContent = "API indisponible";
        });
    }

    // ---- Actions ----
    var activePolls = {};

    var runningTaskIds = {}; // taskType -> taskId

    function startTask(taskType) {
        var btn = document.getElementById("btn-" + taskType);
        var statusEl = document.getElementById("status-" + taskType);

        // If already running, cancel it.
        if (runningTaskIds[taskType]) {
            fetch(API + "/tasks/" + runningTaskIds[taskType], { method: "DELETE", headers: apiHeaders() })
                .then(function () {
                    if (statusEl) { statusEl.textContent = "Arrete"; statusEl.className = "action-status failed"; }
                    setBtnStop(btn, false);
                    delete runningTaskIds[taskType];
                }).catch(function () {});
            return;
        }

        setBtnStop(btn, true);
        if (statusEl) { statusEl.textContent = "Demarrage..."; statusEl.className = "action-status running"; }

        fetch(API + "/tasks/" + taskType, { method: "POST", headers: apiHeaders() })
            .then(function (r) { return r.json(); })
            .then(function (task) {
                if (task.status === "nothing_to_do") {
                    if (statusEl) { statusEl.textContent = task.message; statusEl.className = "action-status completed"; }
                    setBtnStop(btn, false);
                } else if (task.id) {
                    runningTaskIds[taskType] = task.id;
                    pollTask(task.id, taskType);
                } else if (task.error) {
                    if (statusEl) { statusEl.textContent = task.message || task.error; statusEl.className = "action-status failed"; }
                    setBtnStop(btn, false);
                }
            })
            .catch(function (err) {
                if (statusEl) { statusEl.textContent = "Erreur : " + err.message; statusEl.className = "action-status failed"; }
                setBtnStop(btn, false);
            });
    }

    function setBtnStop(btn, running) {
        if (!btn) return;
        btn.disabled = false;
        if (running) {
            btn.textContent = "Arreter";
            btn.style.background = "var(--danger)";
        } else {
            btn.textContent = "Lancer";
            btn.style.background = "";
        }
    }

    function pollTask(taskId, taskType) {
        if (activePolls[taskType]) clearInterval(activePolls[taskType]);

        activePolls[taskType] = setInterval(function () {
            apiFetch("/tasks/" + taskId).then(function (task) {
                var progEl = document.getElementById("prog-" + taskType);
                var statusEl = document.getElementById("status-" + taskType);
                var btn = document.getElementById("btn-" + taskType);

                if (progEl && task.total > 0) {
                    progEl.style.width = Math.round(task.progress / task.total * 100) + "%";
                }
                if (statusEl) {
                    statusEl.textContent = task.message || task.status;
                    statusEl.className = "action-status " + task.status;
                }

                if (task.status === "completed" || task.status === "failed") {
                    clearInterval(activePolls[taskType]);
                    delete activePolls[taskType];
                    delete runningTaskIds[taskType];
                    setBtnStop(btn, false);
                    if (task.status === "completed" && progEl) progEl.style.width = "100%";
                }
            }).catch(function () {
                clearInterval(activePolls[taskType]);
                delete activePolls[taskType];
                delete runningTaskIds[taskType];
            });
        }, 2000);
    }

    function initActions() {
        var tasks = ["categorize", "extract-entities", "index", "audit", "audit-sensitive"];
        tasks.forEach(function (t) {
            var btn = document.getElementById("btn-" + t);
            if (btn) btn.addEventListener("click", function () { startTask(t); });
        });
    }

    // ---- Settings ----
    function loadSettings() {
        switchView("settings");

        // Load current settings.
        apiFetch("/settings").then(function (s) {
            document.getElementById("set-provider").value = s.ai.default_provider || "off";
            document.getElementById("set-endpoint").value = s.ai.local_endpoint || "http://localhost:11434";

            // Load accounts for sync.
            var syncEl = document.getElementById("sync-accounts");
            if (s.accounts && s.accounts.length > 0) {
                syncEl.innerHTML = s.accounts.map(function (a) {
                    return '<div class="sync-account-row">' +
                        '<span class="sync-account-email">' + esc(a.email) + '</span>' +
                        '<div><button class="sync-btn" data-email="' + esc(a.email) + '">Synchroniser</button>' +
                        '<span class="sync-status"></span></div></div>';
                }).join("");
                syncEl.querySelectorAll(".sync-btn").forEach(function (btn) {
                    btn.addEventListener("click", function () { triggerSync(btn); });
                });
            } else {
                // List accounts from stats endpoint.
                apiFetch("/accounts").then(function (accs) {
                    var accounts = accs.accounts || accs || [];
                    if (!accounts.length) {
                        syncEl.innerHTML = '<div style="color:var(--text-dim);font-size:0.85rem;">Aucun compte configure. Utilisez msgvault add-account en ligne de commande.</div>';
                        return;
                    }
                    syncEl.innerHTML = accounts.map(function (a) {
                        var email = a.email || a.identifier || "";
                        return '<div class="sync-account-row">' +
                            '<span class="sync-account-email">' + esc(email) + '</span>' +
                            '<div><button class="sync-btn" data-email="' + esc(email) + '">Synchroniser</button>' +
                            '<span class="sync-status"></span></div></div>';
                    }).join("");
                    syncEl.querySelectorAll(".sync-btn").forEach(function (btn) {
                        btn.addEventListener("click", function () { triggerSync(btn); });
                    });
                }).catch(function () {
                    syncEl.innerHTML = '<div style="color:var(--text-dim);font-size:0.85rem;">Impossible de charger les comptes.</div>';
                });
            }

            // Pre-select current model after loading models list.
            loadOllamaModels(s.ai.local_model, s.ai.local_embed_model);
        }).catch(function (err) {
            document.getElementById("settings-status").textContent = "Erreur : " + err.message;
        });
    }

    function loadOllamaModels(currentModel, currentEmbed) {
        apiFetch("/ollama/models").then(function (models) {
            var sel = document.getElementById("set-model");
            var embedSel = document.getElementById("set-embed-model");

            sel.innerHTML = models.map(function (m) {
                var label = m.name;
                if (m.parameter_size) label += " (" + m.parameter_size + ")";
                var selected = (m.name === currentModel) ? " selected" : "";
                return '<option value="' + esc(m.name) + '"' + selected + '>' + esc(label) + '</option>';
            }).join("");

            embedSel.innerHTML = '<option value="">Meme que le modele</option>' +
                models.map(function (m) {
                    var label = m.name;
                    if (m.parameter_size) label += " (" + m.parameter_size + ")";
                    var selected = (m.name === currentEmbed) ? " selected" : "";
                    return '<option value="' + esc(m.name) + '"' + selected + '>' + esc(label) + '</option>';
                }).join("");

            if (!models.length) {
                sel.innerHTML = '<option value="">Ollama non disponible</option>';
            }
        }).catch(function () {
            document.getElementById("set-model").innerHTML = '<option value="">Ollama non disponible</option>';
        });
    }

    function saveSettings() {
        var update = {
            default_provider: document.getElementById("set-provider").value,
            local_model: document.getElementById("set-model").value,
            local_embed_model: document.getElementById("set-embed-model").value,
            local_endpoint: document.getElementById("set-endpoint").value
        };

        fetch(API + "/settings", {
            method: "PUT",
            headers: apiHeaders(),
            body: JSON.stringify(update)
        }).then(function (r) { return r.json(); }).then(function (d) {
            var statusEl = document.getElementById("settings-status");
            if (d.status === "saved") {
                statusEl.textContent = "Enregistre !";
                statusEl.style.color = "var(--success)";
            } else {
                statusEl.textContent = d.message || "Erreur";
                statusEl.style.color = "var(--danger)";
            }
            setTimeout(function () { statusEl.textContent = ""; }, 3000);
        }).catch(function (err) {
            document.getElementById("settings-status").textContent = "Erreur : " + err.message;
        });
    }

    function triggerSync(btn) {
        var email = btn.getAttribute("data-email");
        btn.disabled = true;
        var statusEl = btn.parentElement.querySelector(".sync-status");
        statusEl.textContent = "Lancement...";

        fetch(API + "/sync-web", {
            method: "POST",
            headers: apiHeaders(),
            body: JSON.stringify({ account: email })
        }).then(function (r) { return r.json(); }).then(function (d) {
            if (d.id) {
                // Poll task status.
                statusEl.textContent = "En cours...";
                var poll = setInterval(function () {
                    apiFetch("/tasks/" + d.id).then(function (task) {
                        statusEl.textContent = task.message || task.status;
                        if (task.status === "completed" || task.status === "failed") {
                            clearInterval(poll);
                            btn.disabled = false;
                            if (task.status === "completed") {
                                statusEl.style.color = "var(--success)";
                                loadStatsBar(); // Refresh stats.
                            } else {
                                statusEl.style.color = "var(--danger)";
                            }
                        }
                    }).catch(function () { clearInterval(poll); btn.disabled = false; });
                }, 3000);
            } else {
                statusEl.textContent = d.message || d.error || "Erreur";
                btn.disabled = false;
            }
        }).catch(function (err) {
            statusEl.textContent = "Erreur : " + err.message;
            btn.disabled = false;
        });
    }

    // ---- Init ----
    function init() {
        initTheme();
        var hash = window.location.hash.slice(1);
        if (hash) { var p = new URLSearchParams(hash); if (p.get("key")) state.apiKey = p.get("key"); }

        document.getElementById("theme-toggle").addEventListener("click", toggleTheme);

        // Sidebar nav.
        document.querySelectorAll(".sidebar-item[data-view]").forEach(function (a) {
            a.addEventListener("click", function (e) {
                e.preventDefault();
                if (a.getAttribute("data-view") === "dashboard") loadDashboard();
                else if (a.getAttribute("data-view") === "messages") loadMessages(1);
                else if (a.getAttribute("data-view") === "actions") switchView("actions");
                else if (a.getAttribute("data-view") === "settings") loadSettings();
            });
        });

        // Search.
        var input = document.getElementById("search-input");
        var clearBtn = document.getElementById("search-clear");
        input.addEventListener("input", function () {
            clearBtn.classList.toggle("hidden", !input.value);
        });
        input.addEventListener("keydown", function (e) {
            if (e.key === "Enter" && input.value.trim()) doSearch(input.value.trim(), 1);
        });
        clearBtn.addEventListener("click", function () {
            input.value = ""; clearBtn.classList.add("hidden"); input.focus();
        });

        // Detail close.
        document.getElementById("detail-close").addEventListener("click", function () {
            document.getElementById("message-detail").classList.add("hidden");
        });
        document.addEventListener("keydown", function (e) {
            if (e.key === "Escape") document.getElementById("message-detail").classList.add("hidden");
        });

        // Actions buttons.
        initActions();

        // Settings save button.
        document.getElementById("btn-save-settings").addEventListener("click", saveSettings);

        // Load.
        loadStatsBar();
        loadSidebar();
        loadDashboard();
    }

    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
    else init();
})();
