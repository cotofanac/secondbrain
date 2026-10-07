// Submit as soon as the eighth digit is entered.
document.querySelector('.passcode-input')?.addEventListener('input', function () {
    if (this.value.length === 8) this.closest('form').requestSubmit();
});
