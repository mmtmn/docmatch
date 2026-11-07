// mdBook's checkbox still owns the sidebar state; the visible control is a native button.
const toggle = document.getElementById('mdbook-sidebar-toggle');
const checkbox = document.getElementById('mdbook-sidebar-toggle-anchor');
toggle?.addEventListener('click', () => {
  checkbox.checked = !checkbox.checked;
  checkbox.dispatchEvent(new Event('change'));
});
