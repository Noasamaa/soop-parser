(() => {
  const $ = (id) => document.getElementById(id);

  const urlInput = $("urlInput");
  const pwdInput = $("pwdInput");
  const tokenInput = $("tokenInput");
  const tokenWrap = $("tokenWrap");
  const proxyToggle = $("proxyToggle");
  const resolveBtn = $("resolveBtn");
  const errorCard = $("errorCard");
  const errorText = $("errorText");
  const resultCard = $("resultCard");
  const titleText = $("titleText");
  const platformTag = $("platformTag");
  const liveTag = $("liveTag");
  const authorTag = $("authorTag");
  const channelTag = $("channelTag");
  const bnoTag = $("bnoTag");
  const qualitiesEl = $("qualities");
  const player = $("player");
  const playHint = $("playHint");
  const configMeta = $("configMeta");
  const copyRow = $("copyRow");
  const playUrlOut = $("playUrlOut");
  const copyBtn = $("copyBtn");
  const catalogEl = $("catalog");
  const catalogMeta = $("catalogMeta");
  const catalogRefresh = $("catalogRefresh");
  const scheduleEl = $("schedule");
  const scheduleMeta = $("scheduleMeta");
  const scheduleRefresh = $("scheduleRefresh");
  const hupuPanel = $("hupuPanel");

  let hls = null;
  let authRequired = false;
  let catalogTimer = null;
  let scheduleTimer = null;

  // 台湾英雄联盟中文解说常用源（频道页，开播时自动取当前场次）
  const DEFAULT_PRESETS = [
    {
      id: "loltw",
      label: "台灣中文",
      badge: "SOOP",
      url: "https://play.sooplive.com/loltw",
      primary: true,
    },
    {
      id: "lckcarry",
      label: "LCK 中文",
      badge: "SOOP",
      url: "https://play.sooplive.com/lckcarry",
    },
    {
      id: "carrylck",
      label: "LCK 中文·備",
      badge: "SOOP",
      url: "https://play.sooplive.com/carrylck",
    },
    {
      id: "yt-lckcarry",
      label: "LCK-Carry",
      badge: "YT",
      url: "https://www.youtube.com/@LCKCarry/live",
    },
    {
      id: "yt-lcp",
      label: "LCP / 太平洋",
      badge: "YT",
      url: "https://www.youtube.com/@lolesportstw/live",
    },
  ];

  const TOKEN_KEY = "live_parser_access_token";
  const PRESET_KEY = "live_parser_last_preset";
  const saved = localStorage.getItem(TOKEN_KEY);
  if (saved) tokenInput.value = saved;

  tokenInput.addEventListener("change", () => {
    const v = tokenInput.value.trim();
    if (v) localStorage.setItem(TOKEN_KEY, v);
    else localStorage.removeItem(TOKEN_KEY);
  });

  function authHeaders() {
    const headers = { "Content-Type": "application/json" };
    const t = tokenInput.value.trim();
    if (t) headers["X-Access-Token"] = t;
    return headers;
  }

  function withToken(url) {
    if (!authRequired) return url;
    const t = tokenInput.value.trim();
    if (!t) return url;
    const sep = url.includes("?") ? "&" : "?";
    return `${url}${sep}token=${encodeURIComponent(t)}`;
  }

  function showError(msg) {
    errorText.textContent = msg;
    errorCard.classList.remove("hidden");
  }

  function clearError() {
    errorCard.classList.add("hidden");
    errorText.textContent = "";
  }

  function destroyPlayer() {
    if (hls) {
      hls.destroy();
      hls = null;
    }
    player.removeAttribute("src");
    player.load();
  }

  function isHlsUrl(url, protocol) {
    if (protocol === "progressive") return false;
    if (protocol === "hls") return true;
    return url.includes(".m3u8") || url.includes("/playlist.m3u8") || url.includes("/api/hls/");
  }

  function setPlayUrlOutput(url) {
    if (!url) {
      copyRow.classList.add("hidden");
      playUrlOut.value = "";
      return;
    }
    copyRow.classList.remove("hidden");
    playUrlOut.value = url;
  }

  function looksLikeCNPlatform(url) {
    const u = (url || "").toLowerCase().trim();
    return u.includes("bilibili.com") || u.includes("huya.com") || /^\d{1,12}$/.test(u);
  }

  /** Proxy flag for API: CN always direct; SOOP/YT use checkbox (default on). */
  function resolveProxyFlag(url) {
    if (looksLikeCNPlatform(url)) return false;
    return proxyToggle.checked;
  }

  function playUrl(url, protocol) {
    destroyPlayer();
    let finalUrl = url;
    if (finalUrl.startsWith("/")) {
      finalUrl = `${location.origin}${finalUrl}`;
    }
    if (authRequired && !/[?&]token=/.test(finalUrl)) {
      finalUrl = withToken(finalUrl);
    }
    setPlayUrlOutput(finalUrl);
    const viaProxy = finalUrl.includes("/api/hls/") || finalUrl.includes("/api/media/");
    const isFlv = protocol === "progressive" || /\.flv(\?|$)/i.test(finalUrl);
    if (viaProxy) {
      playHint.textContent = `播放中（经本站代理）：${finalUrl}`;
    } else if (isFlv) {
      playHint.textContent = `直连 FLV（浏览器通常无法播）：可复制到 PotPlayer / VLC。${finalUrl}`;
    } else {
      playHint.textContent = `播放中（直连 CDN）：${finalUrl}`;
    }

    if (!isHlsUrl(finalUrl, protocol)) {
      player.src = finalUrl;
      player.play().catch(() => {});
      return;
    }

    if (player.canPlayType("application/vnd.apple.mpegurl")) {
      player.src = finalUrl;
      player.play().catch(() => {});
      return;
    }

    if (window.Hls && Hls.isSupported()) {
      hls = new Hls({
        enableWorker: true,
        lowLatencyMode: true,
        xhrSetup: (xhr) => {
          if (!authRequired) return;
          const t = tokenInput.value.trim();
          if (t) xhr.setRequestHeader("X-Access-Token", t);
        },
      });
      hls.loadSource(finalUrl);
      hls.attachMedia(player);
      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        player.play().catch(() => {});
      });
      hls.on(Hls.Events.ERROR, (_, data) => {
        if (data.fatal) {
          showError(`播放失败：${data.type} / ${data.details || "unknown"}`);
        }
      });
      return;
    }

    showError("当前浏览器不支持 HLS 播放，请使用 Chrome / Edge / Safari，或把地址复制到 VLC / PotPlayer。");
  }

  function renderResult(data) {
    resultCard.classList.remove("hidden");
    titleText.textContent = data.title || "(无标题)";
    platformTag.textContent = (data.platform || "unknown").toUpperCase();
    liveTag.textContent = data.is_live ? "LIVE" : "VOD";
    authorTag.textContent = data.author ? `频道 ${data.author}` : "频道 —";
    channelTag.textContent = data.channel ? `ID ${data.channel}` : "";
    bnoTag.textContent = data.bno ? `视频 ${data.bno}` : "";

    qualitiesEl.innerHTML = "";
    const list = data.qualities || [];
    if (!list.length) {
      playHint.textContent = "没有可用清晰度";
      return;
    }

    list.forEach((q, idx) => {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "ghost";
      btn.textContent = q.label || q.name;
      btn.addEventListener("click", () => {
        [...qualitiesEl.querySelectorAll("button")].forEach((b) => b.classList.remove("active"));
        btn.classList.add("active");
        const u = q.play_url || q.direct_url;
        if (!u) {
          showError("该清晰度没有可播放地址");
          return;
        }
        clearError();
        playUrl(u, q.protocol || "hls");
      });
      qualitiesEl.appendChild(btn);
      if (idx === 0) btn.click();
    });
  }

  async function loadConfig() {
    try {
      const res = await fetch(withToken("/api/config"), { headers: authHeaders() });
      if (res.status === 401) {
        authRequired = true;
        tokenWrap.classList.remove("hidden");
        configMeta.textContent = "需要访问令牌才能使用";
        return;
      }
      const data = await res.json();
      authRequired = !!data.auth_required;
      if (authRequired) tokenWrap.classList.remove("hidden");
      else tokenWrap.classList.add("hidden");

      if (Array.isArray(data.presets) && data.presets.length) {
        renderPresets(data.presets);
        // re-apply active highlight after server presets replace DOM
        const cur = urlInput.value.trim();
        const hit = data.presets.find((p) => p.url === cur);
        if (hit) setPresetActive(hit.id);
      }

      const parts = [];
      parts.push((data.platforms || []).join(" + ") || "soop");
      if (data.engine) parts.push(`引擎 ${data.engine}`);
      if (data.public_base_url) parts.push(`公网: ${data.public_base_url}`);
      parts.push(authRequired ? "已启用访问令牌" : "未启用访问令牌");
      parts.push(`会话 ${data.play_token_ttl}s`);
      configMeta.innerHTML = parts.join("<br/>");
    } catch {
      configMeta.textContent = "配置加载失败（服务可能未启动）";
    }
  }

  async function resolve() {
    clearError();
    const url = urlInput.value.trim();
    if (!url) {
      showError("请输入 SOOP / YouTube / B站 / 虎牙 链接");
      return;
    }

    // 展示与后端一致：识别到 B站/虎牙时关代理勾选（不写 userTouched，避免污染 SOOP/YT）
    if (looksLikeCNPlatform(url)) {
      proxyToggle.checked = false;
    }
    const proxy = resolveProxyFlag(url);

    resolveBtn.disabled = true;
    resolveBtn.textContent = "解析中…";
    resultCard.classList.add("hidden");
    destroyPlayer();

    try {
      const res = await fetch("/api/resolve", {
        method: "POST",
        headers: authHeaders(),
        body: JSON.stringify({
          url,
          stream_password: pwdInput.value,
          proxy,
        }),
      });

      const data = await res.json().catch(() => ({}));
      if (!res.ok || data.ok === false) {
        const msg = data.message || data.detail || `HTTP ${res.status}`;
        const code = data.code ? `[${data.code}] ` : "";
        showError(`${code}${typeof msg === "string" ? msg : JSON.stringify(msg)}`);
        return;
      }
      renderResult(data);
    } catch (err) {
      showError(`网络错误：${err.message || err}`);
    } finally {
      resolveBtn.disabled = false;
      resolveBtn.textContent = "解析";
    }
  }

  function setPresetActive(id) {
    document.querySelectorAll(".preset-btn").forEach((b) => {
      b.classList.toggle("active", b.dataset.id === id);
    });
  }

  function setCatalogActive(id) {
    document.querySelectorAll(".cat-card").forEach((b) => {
      b.classList.toggle("active", b.dataset.id === id);
    });
  }

  function esc(s) {
    return String(s ?? "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function platBadge(platform) {
    switch ((platform || "").toLowerCase()) {
      case "huya":
        return "虎牙";
      case "bilibili":
        return "B站";
      case "soop":
        return "SOOP";
      case "youtube":
        return "YT";
      default:
        return (platform || "").toUpperCase();
    }
  }

  function statusBadge(item) {
    if (item.is_replay) return { cls: "replay", text: "重播" };
    if (item.is_live) return { cls: "live", text: "LIVE" };
    return { cls: "off", text: "未开播" };
  }

  function renderCatalog(data) {
    if (!catalogEl) return;
    catalogEl.innerHTML = "";
    const groups = data.groups || [];
    if (!groups.length) {
      catalogMeta.textContent = "暂无目录数据";
      return;
    }

    let liveN = 0;
    let total = 0;
    groups.forEach((g) => {
      const items = g.items || [];
      total += items.length;
      items.forEach((it) => {
        if (it.is_live && !it.is_replay) liveN += 1;
      });

      const section = document.createElement("section");
      section.className = "cat-group";
      const h = document.createElement("h3");
      h.className = "cat-group-title";
      h.textContent = g.title || g.id;
      section.appendChild(h);

      const grid = document.createElement("div");
      grid.className = "cat-grid";

      items.forEach((item) => {
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "cat-card" + (item.is_live && !item.is_replay ? "" : " offline");
        btn.dataset.id = item.id;
        btn.dataset.url = item.url;
        btn.title = item.url;

        const st = statusBadge(item);
        const cover = item.cover
          ? `<img class="cat-cover" src="${esc(item.cover)}" alt="" loading="lazy" referrerpolicy="no-referrer" />`
          : `<div class="cat-cover placeholder">${esc(platBadge(item.platform))}</div>`;

        btn.innerHTML = `
          ${cover}
          <span class="cat-badge ${st.cls}">${esc(st.text)}</span>
          <span class="cat-plat">${esc(platBadge(item.platform))}</span>
          <div class="cat-body">
            <div class="cat-role">${esc(item.role || "")}</div>
            <div class="cat-title">${esc(item.title || item.label || "未命名")}</div>
            <div class="cat-author">${esc(item.author || item.label || "")}</div>
          </div>
        `;
        btn.addEventListener("click", () => {
          if (!item.url) return;
          urlInput.value = item.url;
          setCatalogActive(item.id);
          localStorage.setItem(PRESET_KEY, item.id);
          if (item.is_replay) {
            showError("该房间当前是回放/重播，播放器里容易卡缓冲。已填入链接，可手动解析试试。");
          } else if (!item.is_live && item.platform !== "soop" && item.platform !== "youtube") {
            showError("标记为未开播。仍可解析试试（状态有约 45s 缓存）。");
          } else {
            clearError();
          }
          resolve();
        });
        grid.appendChild(btn);
      });

      section.appendChild(grid);
      catalogEl.appendChild(section);
    });

    const ts = data.updated_at ? new Date(data.updated_at).toLocaleTimeString() : "";
    catalogMeta.textContent = `共 ${total} 路 · LIVE ${liveN} · 封面/标题实时拉取${ts ? " · 更新 " + ts : ""}`;
  }

  async function loadCatalog() {
    if (!catalogEl) return;
    try {
      catalogMeta.textContent = "加载实时封面 / 标题…";
      const res = await fetch(withToken("/api/catalog"), { headers: authHeaders() });
      if (res.status === 401) {
        authRequired = true;
        tokenWrap.classList.remove("hidden");
        catalogMeta.textContent = "需要访问令牌";
        return;
      }
      const data = await res.json();
      if (!res.ok || data.ok === false) {
        catalogMeta.textContent = data.message || "目录加载失败";
        return;
      }
      renderCatalog(data);
    } catch (err) {
      catalogMeta.textContent = "目录加载失败（服务可能未启动）";
    }
  }

  function stateBadge(state) {
    if (state === "inProgress") return { cls: "live", text: "LIVE" };
    if (state === "completed") return { cls: "done", text: "已结束" };
    return { cls: "soon", text: "未开始" };
  }

  function fmtMatchTime(iso) {
    try {
      const d = new Date(iso);
      const md = `${d.getMonth() + 1}/${d.getDate()}`;
      const hm = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
      return `${md}\n${hm}`;
    } catch {
      return "—";
    }
  }

  function fmtDay(iso) {
    try {
      return new Date(iso).toLocaleDateString();
    } catch {
      return "";
    }
  }

  function renderSchedule(data) {
    if (!scheduleEl) return;
    scheduleEl.innerHTML = "";
    const tours = data.tournaments || [];
    if (!tours.length) {
      scheduleMeta.textContent = "当前无可见赛程（已结束超过 3 天的赛事会自动隐藏）";
      return;
    }
    let matchN = 0;
    let liveN = 0;
    tours.forEach((t) => {
      const matches = t.matches || [];
      matchN += matches.length;
      const card = document.createElement("div");
      card.className = "sch-tour";
      const head = document.createElement("div");
      head.className = "sch-tour-head";
      head.innerHTML = `<div class="sch-tour-title">${esc(t.label || t.slug)}</div>
        <div class="sch-tour-range">${esc(fmtDay(t.start_date))} – ${esc(fmtDay(t.end_date))} · 隐藏于 ${esc(fmtDay(t.hide_after))}</div>`;
      card.appendChild(head);

      const list = document.createElement("div");
      list.className = "sch-list";
      if (!matches.length) {
        const empty = document.createElement("div");
        empty.className = "catalog-sub";
        empty.style.padding = "10px 12px";
        empty.textContent = "暂无对阵数据";
        list.appendChild(empty);
      }
      matches.forEach((m) => {
        if (m.state === "inProgress") liveN += 1;
        const teams = m.teams || [];
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "sch-match";
        const st = stateBadge(m.state);
        const teamHTML = teams
          .map((tm) => {
            const img = tm.image
              ? `<img src="${esc(tm.image)}" alt="" loading="lazy" referrerpolicy="no-referrer" />`
              : "";
            return `<div class="sch-team">${img}<span>${esc(tm.code || tm.name || "?")}</span><span class="sch-score">${esc(String(tm.wins ?? 0))}</span></div>`;
          })
          .join("");
        btn.innerHTML = `
          <div class="sch-time">${esc(fmtMatchTime(m.start_time)).replace("\n", "<br/>")}</div>
          <div class="sch-teams">${teamHTML || "—"}</div>
          <div class="sch-meta">
            <span class="sch-state ${st.cls}">${esc(st.text)}</span>
            <span class="sch-bo">BO${esc(m.bo || 1)}${m.block ? " · " + esc(m.block) : ""}</span>
          </div>`;
        btn.addEventListener("click", () => {
          document.querySelectorAll(".sch-match").forEach((el) => el.classList.remove("active"));
          btn.classList.add("active");
          const a = teams[0]?.code || teams[0]?.name || "";
          const b = teams[1]?.code || teams[1]?.name || "";
          loadHupu(a, b, m);
        });
        list.appendChild(btn);
      });
      card.appendChild(list);
      scheduleEl.appendChild(card);
    });
    const ts = data.updated_at ? new Date(data.updated_at).toLocaleTimeString() : "";
    scheduleMeta.textContent = `${tours.length} 项赛事 · ${matchN} 场 · LIVE ${liveN}${ts ? " · 更新 " + ts : ""} · 结束后 3 天隐藏`;
  }

  async function loadSchedule() {
    if (!scheduleEl) return;
    try {
      scheduleMeta.textContent = "加载赛程…";
      const res = await fetch(withToken("/api/schedule"), { headers: authHeaders() });
      if (res.status === 401) {
        authRequired = true;
        tokenWrap.classList.remove("hidden");
        scheduleMeta.textContent = "需要访问令牌";
        return;
      }
      const data = await res.json();
      if (!res.ok || data.ok === false) {
        scheduleMeta.textContent = data.message || "赛程加载失败";
        return;
      }
      renderSchedule(data);
    } catch {
      scheduleMeta.textContent = "赛程加载失败";
    }
  }

  async function loadHupu(teamA, teamB, match) {
    if (!hupuPanel) return;
    hupuPanel.classList.remove("hidden");
    hupuPanel.innerHTML = `<h4>虎扑评分 · ${esc(teamA)} vs ${esc(teamB)}</h4><p class="hupu-msg">拉取评分与热评中…</p>`;
    try {
      const q = new URLSearchParams({ team_a: teamA, team_b: teamB });
      const res = await fetch(withToken("/api/hupu/rating?" + q.toString()), { headers: authHeaders() });
      const data = await res.json();
      const rating = data.rating || data;
      if (!rating || rating.available === false) {
        const msg = rating?.message || data.message || "暂无评分";
        const link = rating?.source_url || "";
        hupuPanel.innerHTML = `<h4>虎扑评分 · ${esc(teamA)} vs ${esc(teamB)}</h4>
          <p class="hupu-msg">${esc(msg)}</p>
          ${link ? `<a class="hupu-link" href="${esc(link)}" target="_blank" rel="noopener">去虎扑搜索</a>` : ""}`;
        return;
      }
      const players = rating.players || [];
      const comments = rating.comments || [];
      const playersHTML = players.length
        ? `<div class="hupu-players">${players
            .map(
              (p) => `<div class="hupu-player"><div class="name">${esc(p.name)}</div>
              <div class="score">${esc(Number(p.score).toFixed(1))}</div>
              <div class="team">${esc(p.team || "")}</div></div>`
            )
            .join("")}</div>`
        : `<p class="hupu-msg">暂无选手评分</p>`;
      const commentsHTML = comments.length
        ? `<div class="hupu-comments">${comments
            .map(
              (c) => `<div class="hupu-comment"><div class="meta">${esc(c.user || "匿名")} · 亮了 ${esc(c.lights || 0)}</div>
              <div>${esc(c.content)}</div></div>`
            )
            .join("")}</div>`
        : `<p class="hupu-msg">暂无热评（或接口未返回评论）</p>`;
      hupuPanel.innerHTML = `<h4>虎扑评分 · ${esc(rating.title || teamA + " vs " + teamB)}</h4>
        ${playersHTML}${commentsHTML}
        ${rating.source_url ? `<a class="hupu-link" href="${esc(rating.source_url)}" target="_blank" rel="noopener">来源 / 虎扑</a>` : ""}`;
    } catch (err) {
      hupuPanel.innerHTML = `<h4>虎扑评分</h4><p class="hupu-msg">加载失败：${esc(err.message || err)}</p>`;
    }
  }

  function renderPresets(list) {
    const el = $("presets");
    if (!el) return;
    el.innerHTML = "";
    list.forEach((p) => {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "preset-btn";
      btn.dataset.id = p.id;
      btn.dataset.url = p.url;
      btn.innerHTML = `${p.label}<span class="badge">${p.badge || ""}</span>`;
      btn.title = p.url;
      btn.addEventListener("click", () => {
        urlInput.value = p.url;
        setPresetActive(p.id);
        localStorage.setItem(PRESET_KEY, p.id);
        resolve();
      });
      el.appendChild(btn);
    });
  }

  resolveBtn.addEventListener("click", resolve);
  urlInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter") resolve();
  });
  urlInput.addEventListener("input", () => {
    if (looksLikeCNPlatform(urlInput.value)) {
      proxyToggle.checked = false;
    }
  });
  copyBtn.addEventListener("click", async () => {
    const v = playUrlOut.value;
    if (!v) return;
    try {
      await navigator.clipboard.writeText(v);
      copyBtn.textContent = "已复制";
      setTimeout(() => {
        copyBtn.textContent = "复制";
      }, 1200);
    } catch {
      playUrlOut.select();
      showError("复制失败，请手动全选输入框");
    }
  });

  const params = new URLSearchParams(location.search);
  if (params.get("token")) {
    tokenInput.value = params.get("token");
    localStorage.setItem(TOKEN_KEY, params.get("token"));
  }

  renderPresets(DEFAULT_PRESETS);

  // 默认：URL 参数 > 上次预设 > 台灣中文 loltw
  const primary = DEFAULT_PRESETS.find((p) => p.primary) || DEFAULT_PRESETS[0];
  const lastId = localStorage.getItem(PRESET_KEY);
  const last = DEFAULT_PRESETS.find((p) => p.id === lastId);
  if (params.get("url")) {
    urlInput.value = params.get("url");
  } else if (last) {
    urlInput.value = last.url;
    setPresetActive(last.id);
  } else if (primary) {
    urlInput.value = primary.url;
    setPresetActive(primary.id);
  }

  if (catalogRefresh) {
    catalogRefresh.addEventListener("click", () => loadCatalog());
  }
  if (scheduleRefresh) {
    scheduleRefresh.addEventListener("click", () => loadSchedule());
  }

  loadConfig();
  loadSchedule();
  loadCatalog();
  // refresh periodically
  catalogTimer = setInterval(loadCatalog, 90_000);
  scheduleTimer = setInterval(loadSchedule, 120_000);
})();
