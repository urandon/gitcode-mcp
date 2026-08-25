(() => {
  let value = null;
  try {
    value = localStorage.getItem('gitcode-mcp-theme');
  } catch {
    // Restricted storage still gets the safe System default.
  }
  const theme = value === 'light' || value === 'dark' ? value : 'system';
  document.documentElement.dataset.theme = theme;
})();
