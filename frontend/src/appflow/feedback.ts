

/**
 * Setup feedback mailto link with template
 */
export function setupFeedbackLink(): void {
  const feedbackLink = document.getElementById('feedback-link') as HTMLAnchorElement;
  if (!feedbackLink) return;

  const feedbackEmail = 'contact@leanercloud.com';
  const subject = 'CUDly Feedback';

  const refreshFeedbackLink = (): void => {
    const body = `Hi CUDly Team,

I'd like to share some feedback about CUDly:


## Feedback Type

[ ] Bug Report
[ ] Feature Request
[ ] General Feedback
[ ] Question


## Description

Please describe your feedback in detail:



## Steps to Reproduce (for bugs)

1.
2.
3.


## Expected vs Actual Behavior (for bugs)

Expected:
Actual:


## Screenshots

Please attach any relevant screenshots to this email.


## Environment

- Browser: ${navigator.userAgent}
- URL: ${window.location.href}
- Date: ${new Date().toISOString()}


---
Thank you for helping us improve CUDly!
`;

    const mailtoUrl = `mailto:${feedbackEmail}?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`;
    feedbackLink.href = mailtoUrl;
  };

  refreshFeedbackLink();
  feedbackLink.onclick = refreshFeedbackLink;
  feedbackLink.onfocus = refreshFeedbackLink;
  feedbackLink.oncontextmenu = refreshFeedbackLink;
}
