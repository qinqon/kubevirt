/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2023 Red Hat, Inc.
 *
 */

package link

import (
	"errors"
	"fmt"

	"github.com/vishvananda/netlink"

	"kubevirt.io/kubevirt/pkg/network/driver"
)

// LinkByName return the pod interface link of the given network name.
// If no link is found, a nil link will be returned.
func LinkByName(handler driver.NetworkHandler, podIfaceName string) (netlink.Link, error) {
	link, err := handler.LinkByName(podIfaceName)
	if err == nil {
		return link, nil
	}
	var linkNotFoundErr netlink.LinkNotFoundError
	if !errors.As(err, &linkNotFoundErr) {
		return nil, fmt.Errorf("could not get link with name %q: %v", podIfaceName, err)
	}

	return link, err
}
