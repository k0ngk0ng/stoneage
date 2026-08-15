"use strict";

(function () {
  const modal = document.getElementById("confirm-modal");
  if (!modal) {
    return;
  }

  const message = modal.querySelector("#confirm-message");
  const accept = modal.querySelector("[data-confirm-accept]");
  const cancelButtons = modal.querySelectorAll("[data-confirm-cancel]");
  let pendingTarget = null;
  let previousFocus = null;

  function closeModal() {
    modal.hidden = true;
    document.body.classList.remove("modal-open");
    pendingTarget = null;
    if (previousFocus && typeof previousFocus.focus === "function") {
      previousFocus.focus();
    }
    previousFocus = null;
  }

  function openModal(target) {
    pendingTarget = target;
    previousFocus = document.activeElement;
    message.textContent = target.dataset.confirm || "确认执行此操作？";
    modal.hidden = false;
    document.body.classList.add("modal-open");
    accept.focus();
  }

  function confirmAction() {
    const target = pendingTarget;
    closeModal();
    if (!target || target.disabled) {
      return;
    }

    const form = target.form || target.closest("form");
    if (form) {
      // requestSubmit keeps native required-field and type validation intact.
      if (typeof form.requestSubmit === "function") {
        const submitter = target.matches("button:not([type]), button[type=submit], input[type=submit], input[type=image]") ? target : null;
        if (submitter) {
          form.requestSubmit(submitter);
        } else {
          form.requestSubmit();
        }
      } else {
        form.submit();
      }
      return;
    }

    if (target.matches("a[href]")) {
      window.location.assign(target.href);
    }
  }

  document.addEventListener("click", function (event) {
    const target = event.target instanceof Element ? event.target.closest("[data-confirm]") : null;
    if (!target || target.disabled) {
      return;
    }
    event.preventDefault();
    openModal(target);
  });

  accept.addEventListener("click", confirmAction);
  cancelButtons.forEach(function (button) {
    button.addEventListener("click", closeModal);
  });
  document.addEventListener("keydown", function (event) {
    if (modal.hidden) {
      return;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      closeModal();
    }
  });
})();
