"use strict";

document.addEventListener("click", function (event) {
  const target = event.target.closest("[data-confirm]");
  if (target && !window.confirm(target.dataset.confirm)) {
    event.preventDefault();
  }
});
