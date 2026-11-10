export const statuses = [
  "open",
  "under_review",
  "planned",
  "approved_for_build",
  "in_progress",
  "shipped",
  "declined",
] as const;
export type Status = (typeof statuses)[number];
export const statusLabels: Record<Status, string> = {
  open: "Open",
  under_review: "Under review",
  planned: "Planned",
  approved_for_build: "Approved for build",
  in_progress: "In progress",
  shipped: "Shipped",
  declined: "Declined",
};
export interface User {
  user_id: string;
  email: string;
  name: string;
  role: "admin" | "member";
  csrf_token: string;
}
export interface Page<T> {
  items: T[];
  limit: number;
  offset: number;
}
export interface Project {
  id: string;
  slug: string;
  name: string;
  description: string;
  public_status_page: boolean;
}
export interface Comment {
  id: string;
  body: string;
  internal?: boolean;
  author_name?: string;
  created_at: string;
}
export interface History {
  id: string;
  from_status: Status | "";
  to_status: Status;
  actor_name?: string;
  created_at: string;
}
export interface RequestItem {
  id: string;
  project_id: string;
  title: string;
  description: string;
  status: Status;
  source: string;
  votes: number;
  voted?: boolean;
  created_at: string;
  updated_at: string;
  public_token?: string;
  comments?: Comment[];
  history?: History[];
  comments_total?: number;
  history_total?: number;
}
export interface PublicRequest {
  title: string;
  status: Status;
  comments: Comment[];
  history: History[];
  comments_total?: number;
  history_total?: number;
}
export interface ChangelogEntry {
  id: string;
  title: string;
  body: string;
  created_at: string;
  published_at: string | null;
  request_ids: string[];
}
export interface Membership {
  email: string;
}
export interface APIKey {
  id: string;
  name: string;
  key_type: "app" | "assistant";
  app_name: string;
  project_ids: string[];
  created_at: string;
  revoked_at?: string;
}
export interface Webhook {
  id: string;
  url: string;
  events: string[];
  created_at: string;
  disabled_at?: string;
}
export interface Delivery {
  id: string;
  event_type?: string;
  event?: string;
  attempts: number;
  status?: string;
  status_code?: number;
  last_status?: number;
  delivered_at?: string;
  completed_at?: string;
  delivery_log?: {
    attempt: number;
    status: number;
    outcome: string;
    created_at: string;
  }[];
  created_at: string;
  next_attempt_at?: string;
  last_error?: string;
}
