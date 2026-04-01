(function () {
    "use strict";

    var API = "/api/v1";
    var PAGE_SIZE = 50;
    var state = {
        view: "messages",
        page: 1,
        searchPage: 1,
        searchQuery: "",
        apiKey: "",
    };

    // --- Theme ---
    function initTheme() {
        var saved = localStorage.getItem("msgvault-theme");
        if (saved === "light") {
            document.documentElement.setAttribute("data-theme", "light");
        }
    }

    function toggleTheme() {
        var current = document.documentElement.getAttribute("data-theme");
        var next = current === "light" ? "dark" : "light";
        if (next === "dark") {
            document.documentElement.removeAttribute("data-theme");
        } else {
            document.documentElement.setAttribute("data-theme", next);
        }
        localStorage.setItem("msgvault-theme", next);
    }

    // --- API ---
    function apiHeaders() {
        var h = { "Content-Type": "application/json" };
        if (state.apiKey) {
            h["X-API-Key"] = state.apiKey;
        }
        return h;
    }

    function apiFetch(path) {
        return fetch(API + path, { headers: apiHeaders() }).then(function (r) {
            if (!r.ok) throw new Error("HTTP " + r.status);
            return r.json();
        });
    }

    // --- Formatting ---
    function formatDate(iso) {
        if (!iso) return "-";
        var d = new Date(iso);
        var day = String(d.getDate()).padStart(2, "0");
        var month = String(d.getMonth() + 1).padStart(2, "0");
        var year = d.getFullYear();
        var hours = String(d.getHours()).padStart(2, "0");
        var mins = String(d.getMinutes()).padStart(2, "0");
        return day + "/" + month + "/" + year + " " + hours + ":" + mins;
    }

    function formatSize(bytes) {
        if (!bytes || bytes === 0) return "0 B";
        if (bytes < 1024) return bytes + " B";
        if (bytes < 1048576) return (bytes / 1024).toFixed(1) + " Ko";
        return (bytes / 1048576).toFixed(1) + " Mo";
    }

    function escapeHtml(text) {
        var el = document.createElement("span");
        el.textContent = text || "";
        return el.innerHTML;
    }

    // --- Render helpers ---
    function renderMessageItem(msg) {
        var labels = "";
        if (msg.labels && msg.labels.length > 0) {
            labels = '<div class="msg-labels">' +
                msg.labels.map(function (l) {
                    return '<span class="label-badge">' + escapeHtml(l) + '</span>';
                }).join("") +
                '</div>';
        }
        var attachIcon = msg.has_attachments ? '<span class="msg-attachment-icon" title="Pieces jointes">&#128206;</span>' : "";
        return '<div class="message-item" data-id="' + msg.id + '">' +
            '<div>' +
                '<div class="msg-subject">' + escapeHtml(msg.subject || "(sans sujet)") + " " + attachIcon + '</div>' +
                '<div class="msg-from">' + escapeHtml(msg.from || "") + '</div>' +
            '</div>' +
            '<div class="msg-date">' + formatDate(msg.sent_at) + '</div>' +
            '<div class="msg-snippet">' + escapeHtml(msg.snippet || "") + '</div>' +
            labels +
            '</div>';
    }

    function renderPagination(container, page, total, onPage) {
        var totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));
        var html = '<button ' + (page <= 1 ? "disabled" : "") + ' data-page="' + (page - 1) + '">&laquo; Precedent</button>';
        html += '<span class="page-info">Page ' + page + ' / ' + totalPages + ' (' + total + ' resultats)</span>';
        html += '<button ' + (page >= totalPages ? "disabled" : "") + ' data-page="' + (page + 1) + '">Suivant &raquo;</button>';
        container.innerHTML = html;
        container.querySelectorAll("button").forEach(function (btn) {
            btn.addEventListener("click", function () {
                var p = parseInt(btn.getAttribute("data-page"), 10);
                if (p >= 1 && p <= totalPages) onPage(p);
            });
        });
    }

    // --- Views ---
    function loadMessages(page) {
        state.page = page || 1;
        var list = document.getElementById("message-list");
        var pag = document.getElementById("pagination");
        list.innerHTML = '<div class="loading">Chargement...</div>';

        var offset = (state.page - 1) * PAGE_SIZE;
        apiFetch("/messages?page_size=" + PAGE_SIZE + "&page=" + state.page)
            .then(function (data) {
                var messages = data.messages || [];
                var total = data.total || 0;
                if (messages.length === 0) {
                    list.innerHTML = '<div class="empty">Aucun message.</div>';
                    pag.innerHTML = "";
                    return;
                }
                list.innerHTML = messages.map(renderMessageItem).join("");
                renderPagination(pag, state.page, total, loadMessages);
                bindMessageClicks(list);
            })
            .catch(function (err) {
                list.innerHTML = '<div class="empty">Erreur : ' + escapeHtml(err.message) + '</div>';
                pag.innerHTML = "";
            });
    }

    function loadSearch(page) {
        state.searchPage = page || 1;
        var q = state.searchQuery;
        if (!q) return;

        var list = document.getElementById("search-results");
        var pag = document.getElementById("search-pagination");
        list.innerHTML = '<div class="loading">Recherche...</div>';

        apiFetch("/search?q=" + encodeURIComponent(q) + "&page_size=" + PAGE_SIZE + "&page=" + state.searchPage)
            .then(function (data) {
                var messages = data.messages || [];
                var total = data.total || 0;
                if (messages.length === 0) {
                    list.innerHTML = '<div class="empty">Aucun resultat pour "' + escapeHtml(q) + '".</div>';
                    pag.innerHTML = "";
                    return;
                }
                list.innerHTML = messages.map(renderMessageItem).join("");
                renderPagination(pag, state.searchPage, total, loadSearch);
                bindMessageClicks(list);
            })
            .catch(function (err) {
                list.innerHTML = '<div class="empty">Erreur : ' + escapeHtml(err.message) + '</div>';
                pag.innerHTML = "";
            });
    }

    function loadStats() {
        var container = document.getElementById("stats-detail");
        container.innerHTML = '<div class="loading">Chargement...</div>';

        apiFetch("/stats")
            .then(function (data) {
                container.innerHTML =
                    renderStatCard(data.total_messages || 0, "Messages") +
                    renderStatCard(data.total_threads || 0, "Conversations") +
                    renderStatCard(data.total_accounts || 0, "Comptes") +
                    renderStatCard(data.total_labels || 0, "Labels") +
                    renderStatCard(data.total_attachments || 0, "Pieces jointes") +
                    renderStatCard(formatSize(data.database_size_bytes || 0), "Taille base");
            })
            .catch(function (err) {
                container.innerHTML = '<div class="empty">Erreur : ' + escapeHtml(err.message) + '</div>';
            });
    }

    function renderStatCard(value, label) {
        return '<div class="stat-card"><div class="stat-value">' + value + '</div><div class="stat-label">' + label + '</div></div>';
    }

    function loadLabels() {
        var container = document.getElementById("labels-list");
        container.innerHTML = '<div class="loading">Chargement...</div>';

        apiFetch("/stats")
            .then(function () {
                return apiFetch("/aggregates?view_type=labels&sort=count&limit=100");
            })
            .then(function (data) {
                var rows = data.rows || [];
                if (rows.length === 0) {
                    container.innerHTML = '<div class="empty">Aucun label.</div>';
                    return;
                }
                container.innerHTML = rows.map(function (row) {
                    return '<div class="label-card" data-label="' + escapeHtml(row.key) + '">' +
                        '<div class="label-name">' + escapeHtml(row.key) + '</div>' +
                        '<div class="label-count">' + (row.count || 0) + ' messages</div>' +
                        '</div>';
                }).join("");

                container.querySelectorAll(".label-card").forEach(function (card) {
                    card.addEventListener("click", function () {
                        var label = card.getAttribute("data-label");
                        document.getElementById("search-input").value = "label:" + label;
                        state.searchQuery = "label:" + label;
                        switchView("search");
                        loadSearch(1);
                    });
                });
            })
            .catch(function (err) {
                container.innerHTML = '<div class="empty">Erreur : ' + escapeHtml(err.message) + '</div>';
            });
    }

    function loadMessageDetail(id) {
        var panel = document.getElementById("message-detail");
        panel.classList.remove("hidden");

        document.getElementById("detail-subject").textContent = "Chargement...";
        document.getElementById("detail-from").textContent = "";
        document.getElementById("detail-to").textContent = "";
        document.getElementById("detail-date").textContent = "";
        document.getElementById("detail-labels").textContent = "";
        document.getElementById("detail-attachments").innerHTML = "";
        document.getElementById("detail-body").textContent = "";

        apiFetch("/messages/" + id)
            .then(function (msg) {
                document.getElementById("detail-subject").textContent = msg.subject || "(sans sujet)";
                document.getElementById("detail-from").textContent = msg.from || "-";
                document.getElementById("detail-to").textContent = (msg.to || []).join(", ") || "-";
                document.getElementById("detail-date").textContent = formatDate(msg.sent_at);

                var labels = msg.labels || [];
                document.getElementById("detail-labels").innerHTML = labels.map(function (l) {
                    return '<span class="label-badge">' + escapeHtml(l) + '</span>';
                }).join(" ") || "-";

                var attachments = msg.attachments || [];
                if (attachments.length > 0) {
                    document.getElementById("detail-attachments").innerHTML =
                        '<strong>Pieces jointes :</strong> ' +
                        attachments.map(function (a) {
                            return '<span class="attachment-item">' + escapeHtml(a.filename || "sans nom") +
                                ' (' + formatSize(a.size_bytes || 0) + ')</span>';
                        }).join("");
                }

                document.getElementById("detail-body").textContent = msg.body || "(vide)";
            })
            .catch(function (err) {
                document.getElementById("detail-subject").textContent = "Erreur";
                document.getElementById("detail-body").textContent = err.message;
            });
    }

    function bindMessageClicks(container) {
        container.querySelectorAll(".message-item").forEach(function (item) {
            item.addEventListener("click", function () {
                var id = item.getAttribute("data-id");
                loadMessageDetail(id);
            });
        });
    }

    // --- Navigation ---
    function switchView(view) {
        state.view = view;
        document.querySelectorAll(".view").forEach(function (v) {
            v.classList.remove("active");
        });
        document.getElementById("view-" + view).classList.add("active");
        document.querySelectorAll(".tab").forEach(function (t) {
            t.classList.toggle("active", t.getAttribute("data-view") === view);
        });

        if (view === "messages") loadMessages(state.page);
        if (view === "stats") loadStats();
        if (view === "labels") loadLabels();
    }

    // --- Stats bar ---
    function loadStatsBar() {
        apiFetch("/stats")
            .then(function (data) {
                document.getElementById("stats-bar").textContent =
                    (data.total_messages || 0) + " messages | " +
                    (data.total_accounts || 0) + " comptes | " +
                    formatSize(data.database_size_bytes || 0);
            })
            .catch(function () {
                document.getElementById("stats-bar").textContent = "API non disponible";
            });
    }

    // --- Init ---
    function init() {
        initTheme();

        // Check for API key in URL hash (e.g. #key=abc123)
        var hash = window.location.hash.slice(1);
        if (hash) {
            var params = new URLSearchParams(hash);
            if (params.get("key")) {
                state.apiKey = params.get("key");
            }
        }

        // Theme toggle
        document.getElementById("theme-toggle").addEventListener("click", toggleTheme);

        // Tab navigation
        document.querySelectorAll(".tab").forEach(function (tab) {
            tab.addEventListener("click", function () {
                switchView(tab.getAttribute("data-view"));
            });
        });

        // Search
        document.getElementById("search-btn").addEventListener("click", function () {
            state.searchQuery = document.getElementById("search-input").value.trim();
            if (state.searchQuery) loadSearch(1);
        });
        document.getElementById("search-input").addEventListener("keydown", function (e) {
            if (e.key === "Enter") {
                state.searchQuery = this.value.trim();
                if (state.searchQuery) loadSearch(1);
            }
        });

        // Detail close
        document.getElementById("detail-close").addEventListener("click", function () {
            document.getElementById("message-detail").classList.add("hidden");
        });

        // Keyboard: Escape to close detail
        document.addEventListener("keydown", function (e) {
            if (e.key === "Escape") {
                document.getElementById("message-detail").classList.add("hidden");
            }
        });

        // Initial load
        loadStatsBar();
        loadMessages(1);
    }

    if (document.readyState === "loading") {
        document.addEventListener("DOMContentLoaded", init);
    } else {
        init();
    }
})();
