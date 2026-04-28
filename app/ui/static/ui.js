(function () {
  const output = document.getElementById("task-output");
  if (output && output.dataset.outputUrl) {
    const loadOutput = async () => {
      try {
        const response = await fetch(output.dataset.outputUrl + "?line=0", {
          headers: { Accept: "application/json" },
          cache: "no-store",
        });
        if (!response.ok) {
          output.textContent = "Failed to load logs (" + response.status + ")";
          return;
        }
        const payload = await response.json();
        output.textContent = payload.text || "";
      } catch (error) {
        output.textContent = "Failed to load logs";
      }
    };

    loadOutput();
    setInterval(loadOutput, 5000);
  }
})();