/**
 * Password-strength helpers shared by the reset, admin-setup and profile flows.
 */

export function setupPasswordToggle(container: HTMLElement | Document = document): void {
  const toggleButtons = container.querySelectorAll('.toggle-password');

  toggleButtons.forEach(button => {
    button.addEventListener('click', () => {
      const targetId = button.getAttribute('data-target');
      if (!targetId) return;

      const input = document.getElementById(targetId) as HTMLInputElement;
      if (!input) return;

      const isPassword = input.type === 'password';
      input.type = isPassword ? 'text' : 'password';

      // Update aria-label for accessibility
      button.setAttribute('aria-label', isPassword ? 'Hide password' : 'Show password');

      // Toggle eye icon (add slash when password is visible)
      const svg = button.querySelector('.eye-icon');
      if (svg) {
        if (isPassword) {
          // Show eye-off icon (with slash)
          svg.innerHTML = `
            <path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"></path>
            <line x1="1" y1="1" x2="23" y2="23"></line>
          `;
        } else {
          // Show normal eye icon
          svg.innerHTML = `
            <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path>
            <circle cx="12" cy="12" r="3"></circle>
          `;
        }
      }
    });
  });
}

// Special-character set used by every password-strength check in this
// module (live indicator + submit-time validator). Module-level constant
// so the regex isn't recompiled on every keystroke AND so the live
// indicator and validator can't silently drift apart (#470 review).
const SPECIAL_CHAR_RE = /[!@#$%^&*()_+\-=\[\]{};':"\\|,.<>\/?]/;

export function updatePasswordRequirements(password: string, prefix = 'req-'): void {
  const requirements = {
    length: password.length >= 12,
    uppercase: /[A-Z]/.test(password),
    lowercase: /[a-z]/.test(password),
    number: /[0-9]/.test(password),
    special: SPECIAL_CHAR_RE.test(password)
  };

  // Update each requirement indicator
  updateRequirement(`${prefix}length`, requirements.length);
  updateRequirement(`${prefix}uppercase`, requirements.uppercase);
  updateRequirement(`${prefix}lowercase`, requirements.lowercase);
  updateRequirement(`${prefix}number`, requirements.number);
  updateRequirement(`${prefix}special`, requirements.special);
}

/**
 * Return a user-facing description of which password complexity rules
 * the supplied password fails, or "" when the password satisfies every
 * rule. Length is treated as its own message (an empty password isn't
 * missing "one of N character classes", it's just too short); the
 * complexity rules are joined into a single sentence that names ONLY
 * the rules that actually failed, in priority order. Closes issue #458.
 */
export function describePasswordValidationError(password: string): string {
  if (password.length < 12) {
    return 'Password must be at least 12 characters long';
  }
  const missing: string[] = [];
  if (!/[A-Z]/.test(password)) missing.push('one uppercase letter');
  if (!/[a-z]/.test(password)) missing.push('one lowercase letter');
  if (!/[0-9]/.test(password)) missing.push('one number');
  if (!SPECIAL_CHAR_RE.test(password)) missing.push('one special character');
  if (missing.length === 0) return '';
  if (missing.length === 1) return `Password must contain ${missing[0]}`;
  const last = missing.pop()!;
  return `Password must contain ${missing.join(', ')} and ${last}`;
}

function updateRequirement(id: string, isMet: boolean): void {
  const element = document.getElementById(id);
  if (!element) return;

  const icon = element.querySelector('.req-icon');
  if (!icon) return;

  if (isMet) {
    element.classList.add('met');
    element.classList.remove('unmet');
    icon.textContent = '✓';
  } else {
    element.classList.add('unmet');
    element.classList.remove('met');
    icon.textContent = '○';
  }
}
