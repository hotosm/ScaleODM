(function () {
  const TERMINAL = new Set(["completed", "failed", "canceled"]);
  const SVG_NS = "http://www.w3.org/2000/svg";
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

    const svgIcon = (paths) => {
      const svg = document.createElementNS(SVG_NS, "svg");
      svg.setAttribute("viewBox", "0 0 24 24");
      svg.setAttribute("fill", "none");
      svg.setAttribute("stroke", "currentColor");
      svg.setAttribute("stroke-width", "2");
      svg.setAttribute("stroke-linecap", "round");
      svg.setAttribute("stroke-linejoin", "round");
      svg.setAttribute("aria-hidden", "true");
      for (const d of paths) {
        const path = document.createElementNS(SVG_NS, "path");
        path.setAttribute("d", d);
        svg.appendChild(path);
      }
      return svg;
    };

    // Two overlapping sheets, then a tick once the URL is on the clipboard.
    const copyIcon = () =>
      svgIcon([
        "M9 9h10v12H9z",
        "M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1",
      ]);
    const doneIcon = () => svgIcon(["M20 6 9 17l-5-5"]);

    // navigator.clipboard exists only in a secure context, and this UI is often
    // reached over plain HTTP on an internal address, so keep the old
    // execCommand path as the fallback rather than failing the copy.
    const copyText = async (text) => {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
        return;
      }
      const scratch = document.createElement("textarea");
      scratch.value = text;
      scratch.setAttribute("readonly", "");
      scratch.style.position = "fixed";
      scratch.style.opacity = "0";
      document.body.appendChild(scratch);
      scratch.select();
      try {
        if (!document.execCommand("copy")) {
          throw new Error("copy command rejected");
        }
      } finally {
        document.body.removeChild(scratch);
      }
    };

    const copyButton = (url, name) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "copy-url";
      button.title = "Copy download URL";
      button.setAttribute("aria-label", "Copy download URL for " + name);
      button.appendChild(copyIcon());

      let reset;
      const flash = (message, ok) => {
        button.textContent = "";
        button.appendChild(ok ? doneIcon() : copyIcon());
        button.classList.toggle("copied", ok);
        button.classList.toggle("copy-failed", !ok);
        button.title = message;
        clearTimeout(reset);
        reset = setTimeout(() => {
          button.textContent = "";
          button.appendChild(copyIcon());
          button.classList.remove("copied", "copy-failed");
          button.title = "Copy download URL";
        }, 2000);
      };

      button.addEventListener("click", async () => {
        try {
          await copyText(url);
          flash("Copied!", true);
        } catch (error) {
          flash("Could not copy - select the link and copy it manually", false);
        }
      });
      return button;
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
          // Absolute, so the copied URL is usable outside this page - it is what
          // gets pasted into another service's "import from URL" form.
          const absolute = new URL(asset.url, window.location.href).href;
          const li = document.createElement("li");
          const link = document.createElement("a");
          link.href = asset.url;
          link.textContent = asset.name;
          li.appendChild(link);
          li.appendChild(copyButton(absolute, asset.name));
          downloads.appendChild(li);
        }
      } catch (error) {
        renderMessage("Could not check available downloads.");
      }
    };
    poll(downloads, loadAssets, 15000);
  }
})();
