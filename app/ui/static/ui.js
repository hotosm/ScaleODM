(function () {
  const TERMINAL = new Set(["completed", "failed", "canceled"]);
  const pad = (n) => String(n).padStart(2, "0");

  // Load once, then refresh every ms until the task is terminal - a finished
  // task's logs and outputs no longer change, so polling can stop.
  const poll = (el, load, ms) => {
    load();
    if (!TERMINAL.has((el.dataset.status || "").toLowerCase())) {
      setInterval(load, ms);
    }
  };

  // --- Logs ---------------------------------------------------------------
  const output = document.getElementById("task-output");
  const updated = document.getElementById("log-updated");
  const wrapToggle = document.getElementById("log-wrap");

  if (wrapToggle && output) {
    wrapToggle.addEventListener("change", () =>
      output.classList.toggle("wrap", wrapToggle.checked)
    );
  }

  if (output && output.dataset.outputUrl) {
    const loadOutput = async () => {
      // Replacing textContent resets scroll to the top, so keep the reader
      // where they are and only follow new output when already at the bottom.
      const atBottom =
        output.scrollHeight - output.scrollTop - output.clientHeight < 20;
      const prevScroll = output.scrollTop;
      try {
        const response = await fetch(output.dataset.outputUrl, {
          headers: { Accept: "text/plain" },
          cache: "no-store",
        });
        if (!response.ok) {
          output.textContent = "Failed to load logs (" + response.status + ")";
          return;
        }
        output.textContent = await response.text();
        output.scrollTop = atBottom ? output.scrollHeight : prevScroll;
        if (updated) {
          const t = new Date();
          updated.textContent = `Updated ${pad(t.getHours())}:${pad(t.getMinutes())}:${pad(t.getSeconds())}`;
        }
      } catch (error) {
        output.textContent = "Failed to load logs";
      }
    };
    poll(output, loadOutput, 5000);
  }

  // --- Downloads ----------------------------------------------------------
  // Read from the UI detail endpoint (under /ui, so it works through the
  // UI-only ingress); its assets field is null until the task completes.
  const downloads = document.getElementById("task-downloads");
  if (downloads && downloads.dataset.detailUrl) {
    const renderMessage = (msg) => {
      downloads.textContent = "";
      const li = document.createElement("li");
      li.className = "downloads-hint";
      li.textContent = msg;
      downloads.appendChild(li);
    };

    const loadAssets = async () => {
      try {
        const response = await fetch(downloads.dataset.detailUrl, {
          headers: { Accept: "application/json" },
          cache: "no-store",
        });
        if (!response.ok) {
          renderMessage("Could not check available downloads (" + response.status + ").");
          return;
        }
        const assets = (await response.json()).assets || [];
        if (!assets.length) {
          renderMessage("Downloads will appear here once processing completes.");
          return;
        }
        downloads.textContent = "";
        for (const asset of assets) {
          const li = document.createElement("li");
          const link = document.createElement("a");
          link.href = asset.url;
          link.textContent = asset.name;
          li.appendChild(link);
          downloads.appendChild(li);
        }
      } catch (error) {
        renderMessage("Could not check available downloads.");
      }
    };
    poll(downloads, loadAssets, 15000);
  }
})();
