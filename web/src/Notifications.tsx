import { api, useAction, useResource } from "./api";
import { Loading, Notice } from "./ui";

interface Preferences {
  enabled: boolean;
  available: boolean;
}

export function NotificationPreferences() {
  const preferences = useResource<Preferences>("/api/v1/me/notifications");
  const action = useAction();
  return (
    <section>
      <p className="eyebrow">STAY IN THE LOOP</p>
      <h1>Email notifications</h1>
      <p className="lede">Follow the ideas you care about, on your terms.</p>
      <div className="panel">
        <h2>Your email preferences</h2>
        <p>
          Get status changes and new public comments for requests you author,
          vote for, or subscribe to. Administrators also receive new requests.
        </p>
        <Notice
          error={preferences.error ?? action.error}
          success={action.success}
        />
        {preferences.loading ? (
          <Loading />
        ) : (
          preferences.data && (
            <>
              {!preferences.data.available && (
                <p className="hint">
                  Email delivery is not enabled for this service yet. Your
                  preference will be saved.
                </p>
              )}
              <label className="checkbox">
                <input
                  type="checkbox"
                  checked={preferences.data.enabled}
                  disabled={action.busy}
                  onChange={(event) => {
                    const enabled = event.target.checked;
                    void action.run(
                      async () => {
                        await api("/api/v1/me/notifications", "PUT", {
                          enabled,
                        });
                        preferences.reload();
                      },
                      enabled
                        ? "Email notifications enabled."
                        : "Email notifications turned off.",
                    );
                  }}
                />
                Send me email notifications
              </label>
              <p className="hint">
                Turn this off to stop all notification emails. You can also
                unsubscribe using the link in any email.
              </p>
            </>
          )
        )}
      </div>
    </section>
  );
}

export function RequestSubscription({ id }: { id: string }) {
  const subscription = useResource<{ subscribed: boolean }>(
    `/api/v1/requests/${id}/subscription`,
  );
  const action = useAction();
  return (
    <div className="subscription">
      <Notice
        error={subscription.error ?? action.error}
        success={action.success}
      />
      {subscription.loading ? (
        <Loading />
      ) : (
        subscription.data && (
          <>
            <button
              disabled={action.busy}
              aria-pressed={subscription.data.subscribed}
              onClick={() => {
                const subscribed = !subscription.data?.subscribed;
                void action.run(
                  async () => {
                    await api(
                      `/api/v1/requests/${id}/subscription`,
                      subscribed ? "PUT" : "DELETE",
                    );
                    subscription.reload();
                  },
                  subscribed
                    ? "Following this request."
                    : "Updates for this request turned off.",
                );
              }}
            >
              {action.busy
                ? "Saving..."
                : subscription.data.subscribed
                  ? "Following: stop updates"
                  : "Follow request"}
            </button>
            <p className="hint">
              Authors and voters follow automatically. Email updates respect
              your <a href="#view=notifications">email preferences</a>.
            </p>
          </>
        )
      )}
    </div>
  );
}
