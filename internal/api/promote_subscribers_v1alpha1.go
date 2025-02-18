package api

import (
	"context"
	goerrors "errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	typesv1alpha1 "github.com/akuity/kargo/internal/api/types/v1alpha1"
	"github.com/akuity/kargo/internal/kargo"
	"github.com/akuity/kargo/internal/logging"
	svcv1alpha1 "github.com/akuity/kargo/pkg/api/service/v1alpha1"
	"github.com/akuity/kargo/pkg/api/v1alpha1"
)

func (s *server) PromoteSubscribers(
	ctx context.Context,
	req *connect.Request[svcv1alpha1.PromoteSubscribersRequest],
) (*connect.Response[svcv1alpha1.PromoteSubscribersResponse], error) {
	if err := validateProjectAndStageNonEmpty(req.Msg.GetProject(), req.Msg.GetStage()); err != nil {
		return nil, err
	}
	if err := s.validateProject(ctx, req.Msg.GetProject()); err != nil {
		return nil, err
	}
	stage, err := getStage(ctx, s.client, req.Msg.GetProject(), req.Msg.GetStage())
	if err != nil {
		return nil, err
	}
	freightToPromote, err := validateFreightExists(req.Msg.GetFreight(), stage.Status.History)
	if err != nil {
		return nil, err
	}
	if !freightToPromote.Qualified {
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("Cannot promote unqualified freight"),
		)
	}

	subscribers, err := s.findStageSubscribers(ctx, stage)
	if err != nil {
		return nil, err
	}
	if len(subscribers) == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("Stage %q has no subscribers", req.Msg.GetStage()))
	}

	logger := logging.LoggerFromContext(ctx)

	promoteErrs := make([]error, 0, len(subscribers))
	createdPromos := make([]*v1alpha1.Promotion, 0, len(subscribers))
	for _, subscriber := range subscribers {
		if _, err := validateFreightExists(req.Msg.GetFreight(), subscriber.Status.AvailableFreight); err != nil {
			// TODO(JS): currently we create promotions to all of this Stage's
			// subscribers, ignoring whether or not the freight *also* appears in the
			// availableFreight of the subscriber. Normally, it should always be the
			// case that if it's in our history, it should also appear in our
			// subscriber's availableFreight. For now, just log a warning if we are
			// promoting something that for some reason, has not yet appeared there.
			logger.Warnf("Freight '%s' does not appear in available Freight of '%s'", req.Msg.GetFreight(), subscriber.Name)
		}
		newPromo := kargo.NewPromotion(subscriber, req.Msg.GetFreight())
		if err := s.client.Create(ctx, &newPromo); err != nil {
			promoteErrs = append(promoteErrs, err)
			continue
		}
		createdPromos = append(createdPromos, typesv1alpha1.ToPromotionProto(newPromo))
	}

	return connect.NewResponse(&svcv1alpha1.PromoteSubscribersResponse{
		Promotions: createdPromos,
	}), goerrors.Join(promoteErrs...)
}

// findStageSubscribers returns a list of Stages that are subscribed to the given Stage
// TODO: this could be powered by an index.
func (s *server) findStageSubscribers(ctx context.Context, stage *kargoapi.Stage) ([]kargoapi.Stage, error) {
	var allStages kargoapi.StageList
	if err := s.client.List(ctx, &allStages, client.InNamespace(stage.Namespace)); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var subscribers []kargoapi.Stage
	for _, s := range allStages.Items {
		s := s
		if s.Spec.Subscriptions == nil {
			continue
		}
		for _, upstream := range s.Spec.Subscriptions.UpstreamStages {
			if upstream.Name != stage.Name {
				continue
			}
			subscribers = append(subscribers, s)
		}
	}
	return subscribers, nil
}
