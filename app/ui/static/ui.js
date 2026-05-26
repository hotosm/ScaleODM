(function () {
  const output = document.getElementById("task-output");
  if (output && output.dataset.outputUrl) {
    const loadOutput = async () => {
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
      } catch (error) {
        output.textContent = "Failed to load logs";
      }
    };

    loadOutput();
    setInterval(loadOutput, 5000);
  }
})();