/**
 * What a place is running, read from what a deploy module said about it.
 *
 * Out of the component because this is arithmetic, not drawing, and arithmetic that has
 * to be right about a pod with more than one container is exactly the kind that is worth
 * being able to ask about without a browser. A rollout of a one-image workload was right
 * here by accident: the workload's image and a pod's image were the same string, so every
 * comparison below happened to hold. A workload with two containers puts two images in one
 * field and one image in each row of another, and every comparison that was written as
 * `a === b` quietly became false — the badge that says a pod is still on the image being
 * taken off stayed lit on a workload that had finished rolling out minutes earlier, and
 * nothing a person did would ever put it out.
 */

/** One image and how many pods are on it. */
export interface PlacePods {
  image: string
  pods: number
  tags?: string[]
}

/** What `/deploy-current` answered about one place. */
export interface CurrentAnswer {
  known?: boolean
  /** What the workload is set to. Several images, comma-separated, when it has several containers. */
  image?: string
  tags?: string[]
  workload?: string
  ready?: number
  desired?: number
  settled?: boolean
  /** What the pods are actually running, one entry per image — one per container. */
  pods?: PlacePods[]
  asked?: string
  reason?: string
}

/**
 * The images this workload is set to, one per container.
 *
 * A workload with several containers is set to several images, and they arrive in one
 * field separated by commas. Taken as one string, that value is not any image, so it can
 * never be found among the images the pods are on — which is what a search for it does.
 */
export function workloadImages(answer: CurrentAnswer | undefined | null): string[] {
  if (!answer?.image) return []
  return answer.image.split(',').map((one) => one.trim()).filter(Boolean)
}

/**
 * What the pods here are on, by image.
 *
 * From the module's own figures where it gave them. Where it gave none, one entry per
 * image the workload is set to, each carrying the whole of the place's replicas: that is
 * what can be said without inventing a split nobody reported, and it is enough to name
 * what runs.
 */
export function podsOn(answer: CurrentAnswer | undefined | null): PlacePods[] {
  if (!answer?.known) return []
  const pods = answer.pods ?? []
  if (pods.length > 0) return pods
  return workloadImages(answer).map((image) => ({ image, pods: answer?.desired ?? 0 }))
}

/** The images of this workload that have at least one pod on them. */
export function onWanted(answer: CurrentAnswer | undefined | null): PlacePods[] {
  const wanted = new Set(workloadImages(answer))
  return podsOn(answer).filter((one) => wanted.has(one.image))
}

/** How many pods are on the images this workload is set to, counted together. */
export function wantedPods(answer: CurrentAnswer | undefined | null): number {
  return onWanted(answer).reduce((sum, one) => sum + one.pods, 0)
}

/**
 * The image on the most pods here — what this place is actually being served by.
 *
 * One image, because this is the one that had to be named before a pod had more than one
 * container in it, and nothing on a page takes a list where this goes. With several
 * images the answer is whichever the module listed first among those with as many pods:
 * there is no single one of them that the place is more of.
 */
export function servedBy(answer: CurrentAnswer | undefined | null): string {
  const pods = podsOn(answer)
  if (pods.length === 0) return ''
  return pods.reduce((a, b) => (b.pods > a.pods ? b : a)).image
}

/**
 * The image this place is being put on, as soon as one pod is on it.
 *
 * The workload's own image, not the one with the most pods: a rollout that has put one of
 * three pods on the new image is already a rollout onto that image, and calling the place
 * "running" the old one until the last pod turns over says the opposite of what is
 * happening for the whole minute it takes.
 *
 * All of it and not any of it. A workload with two containers is not halfway onto its new
 * images because one of the two has a pod — that is a pod that has not finished coming
 * up, and a page that calls the place "coming on" at that moment is reporting a rollout
 * that has not begun.
 */
export function comingOn(answer: CurrentAnswer | undefined | null): string {
  if (!answer?.known || !answer.image) return ''
  const wanted = workloadImages(answer)
  const on = new Set(podsOn(answer).map((one) => one.image))
  const started = wanted.length > 0 && wanted.every((image) => on.has(image))
  return started ? answer.image : ''
}

/**
 * The one line about what is here: the image being put on where a rollout has begun,
 * and the one it is being served from otherwise.
 */
export function primaryImage(answer: CurrentAnswer | undefined | null): string {
  return comingOn(answer) || servedBy(answer)
}

/**
 * The image being taken off this place, if a rollout is taking one off.
 *
 * A pod on an image this workload is no longer set to: on its way out and still serving
 * traffic, which is the one state that is neither "running here" nor "gone" and that a
 * page marking images in two colours has to be able to say.
 *
 * Membership of the workload's own set of images, and not equality with one of them.
 * Compared as strings, every container of a running pod except the one that happened to be
 * named first looked like an image the place was being taken off — so a two-container
 * workload reported itself as retiring its own second container, for ever, with the count
 * of pods that were serving perfectly well. The badge said "0 of 1" on a rollout that had
 * been over for seven minutes, and could not be argued out of it.
 *
 * Nothing at all when the workload names no image: with nothing to compare against, every
 * pod looks like an image from somewhere else, and saying so would be inventing an answer
 * rather than reporting one.
 */
export function replacing(answer: CurrentAnswer | undefined | null): string {
  if (!answer?.known) return ''
  const wanted = new Set(workloadImages(answer))
  if (wanted.size === 0) return ''
  const outgoing = podsOn(answer).filter((one) => !wanted.has(one.image))
  if (outgoing.length === 0) return ''
  return outgoing.reduce((a, b) => (b.pods > a.pods ? b : a)).image
}

/** How many pods are on the image this place is being taken off. */
export function replacingPods(answer: CurrentAnswer | undefined | null): number {
  const leaving = replacing(answer)
  if (!leaving) return 0
  return podsOn(answer).find((one) => one.image === leaving)?.pods ?? 0
}

/**
 * The names this place's images were published under, from the pods themselves.
 *
 * Every image the workload is set to, not one of them: the tags of a two-container
 * workload are two lists, and showing one container's tags under the other is a claim
 * about the wrong image.
 */
export function wantedTags(answer: CurrentAnswer | undefined | null): string[] {
  const names = onWanted(answer).flatMap((one) => one.tags ?? [])
  return names.length > 0 ? names : answer?.tags ?? []
}
